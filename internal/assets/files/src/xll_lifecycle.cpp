#include "xll_lifecycle.h"
#include "xll_log.h"
#include "xll_excel.h"
#include "xll_launch.h"
#include "xll_worker.h"
#include "xll_ipc.h"
#include "xll_deferred_commands.h" // CancelDeferredRunner (cancel pending xlcOnTime on teardown, #3)
#include "types/mem.h"
#include "com/ribbon_addin.h" // WaitForCommandDrain (declared outside XLL_RIBBON_ENABLED)
#include <cwchar>
#include <thread>  // std::thread g_monitorThread member (joined in Phase 2)
#include <atomic>  // g_destructiveDone / g_rtdServerTerminated CAS + signal
#ifdef XLL_RTD_ENABLED
#include "xll_rtd.h"
#endif

using namespace xll;

// Global Handle
HINSTANCE g_hModule = NULL;
// Global Error Value
XLOPER12 g_xlErrValue;
// Global #GETTING_DATA sentinel (see xll_lifecycle.h). Initialized in DllMain
// alongside g_xlErrValue.
XLOPER12 g_xlErrGettingData;
// Global #N/A sentinel (see xll_lifecycle.h). Initialized in DllMain alongside
// g_xlErrValue.
XLOPER12 g_xlErrNA;

namespace xll {
    // Unloading Flag
    std::atomic<bool> g_isUnloading(false);

    // Process Information for Server
    ProcessInfo g_procInfo = { 0 };

    std::thread g_monitorThread;

    // Single-shot guard for GracefulTeardownOnce(): set with a CAS so the heavy
    // graceful teardown body runs EXACTLY ONCE no matter how many of
    // {OnBeginShutdown, OnDisconnection(HostShutdown), OnDisconnection(UserClosed)}
    // fire on a real quit / add-in-disable (Excel may deliver more than one).
    static std::atomic<bool> g_teardownDone(false);

    // Optional COM/ribbon/RTD destructive-teardown hook, registered by the
    // generated template TU (xll_main.cpp) at xlAutoOpen when a ribbon/command
    // or RTD COM add-in exists. Runs INSIDE GracefulTeardownOnce (so it executes
    // exactly once, on a CONFIRMED real teardown — never on a cancelled quit).
    // It performs the steps that must live in the template TU because they touch
    // template-local symbols (g_ribbonCookie, g_rtdCookie, SetRibbonConnected,
    // CoRevokeClassObject, UnregisterOfficeAddinKey, ShutdownRibbonImageEngine):
    // ribbon disconnect + class-object revoke + registry unregister. Keeping it a
    // function pointer keeps xll_lifecycle.cpp decoupled from the ribbon/RTD TUs.
    //
    // The bool argument is revokeRtdClassObject: false on a host shutdown (skip
    // the RTD CoRevokeClassObject so Excel can complete its RTD teardown
    // handshake — see GracefulTeardownOnce / AGENTS.md §23.6), true otherwise.
    static void (*g_teardownHook)(bool) = nullptr;
    void SetGracefulTeardownHook(void (*hook)(bool)) { g_teardownHook = hook; }

    // Set true by RtdServer::ServerTerminate (via SetRtdServerTerminated). On a
    // CONFIRMED host shutdown the destructive teardown is DEFERRED out of
    // OnBeginShutdown (Phase 1 returns fast) so Excel can run its RTD handshake
    // (DisconnectData on every live topic, then ServerTerminate) WHILE g_phost is
    // still alive. This flag records that the handshake completed; it is retained
    // for diagnosability / idempotence even though Phase 2 is now TRIGGERED directly
    // from inside ServerTerminate (on the STA) rather than polled by a watcher
    // thread (§23.6 Stage 4 remediation, 2026-06-17). See AGENTS.md §23.6.
    static std::atomic<bool> g_rtdServerTerminated(false);
    void SetRtdServerTerminated() {
        g_rtdServerTerminated.store(true, std::memory_order_release);
    }

    // Phase-2 single-shot guard. The destructive teardown (RunDestructiveTeardown,
    // below) may be reached from TWO sites: RtdServer::ServerTerminate on the STA
    // (host-shutdown deferred path), and GracefulTeardownOnce itself synchronously
    // (the non-host-shutdown / add-in-disable path). This CAS makes the destructive
    // body run EXACTLY ONCE regardless of which arrives first. It is SEPARATE from
    // g_teardownDone (which guards Phase-1 entry) because on a host shutdown Phase 1
    // completes and returns while Phase 2 is still pending Excel's RTD handshake.
    static std::atomic<bool> g_destructiveDone(false);

    // Thread for monitoring server process
    void MonitorThread(std::wstring logPath) {
        // If unloading has already started, return immediately to avoid touching
        // global resources that may be freed during a forced unload.
        if (g_isUnloading) return;

        // Run the monitor; MonitorProcess should honor the shutdown event.
        MonitorProcess(g_procInfo, logPath);
    }
}

int xll::RegisterFunction(
    const XLOPER12& xDLL,
    const std::wstring& procedure,
    const std::wstring& typeText,
    const std::wstring& functionText,
    const std::wstring& argumentText,
    int macroType,
    const std::wstring& category,
    const std::wstring& shortcut,
    const std::wstring& helpTopic,
    const std::wstring& functionHelp,
    const std::vector<std::wstring>& argumentHelp,
    XLOPER12& xRegId
) {
    // Prepare pointers for Excel12v
    std::vector<LPXLOPER12> argPtrs;
    argPtrs.reserve(11 + argumentHelp.size());

    // 1. Module Name - Pass DIRECTLY to avoid Double-Free issues with ScopedXLOPER12 copy
    argPtrs.push_back((LPXLOPER12)&xDLL);

    // Helper vector to manage lifecycle of other arguments
    std::vector<ScopedXLOPER12> args;
    args.reserve(10 + argumentHelp.size());

    auto addArg = [&](const auto& val) {
        args.emplace_back(val);
        argPtrs.push_back(args.back());
    };

    // 2. Procedure
    addArg(procedure);

    // 3. Type Text
    addArg(typeText);

    // 4. Function Text
    addArg(functionText);

    // 5. Argument Text
    addArg(argumentText);

    // 6. Macro Type
    addArg(macroType);

    // 7. Category
    addArg(category);

    // 8. Shortcut
    addArg(shortcut);

    // 9. Help Topic
    addArg(helpTopic);

    // 10. Function Description
    addArg(functionHelp);

    // 11+. Argument Descriptions
    for (const auto& help : argumentHelp) {
        addArg(help);
    }

    return Excel12v(xlfRegister, &xRegId, (int)argPtrs.size(), argPtrs.data());
}

// Log Handler for SHM
#ifdef SHM_DEBUG
void LogHandler(shm::LogLevel level, const std::string& msg) {
    LogInfo("[SHM] " + msg);
}
#endif

// Entry point
BOOL APIENTRY DllMain(HINSTANCE hModule, DWORD  ul_reason_for_call, LPVOID lpReserved) {
    XLL_SAFE_BLOCK_BEGIN
        switch (ul_reason_for_call) {
        case DLL_PROCESS_ATTACH:
            g_hModule = hModule;
            // Initialize Global Error Value
            g_xlErrValue.xltype = xltypeErr;
            g_xlErrValue.val.err = xlerrValue;
            // Initialize the #GETTING_DATA first-paint sentinel for rtd-once.
            g_xlErrGettingData.xltype = xltypeErr;
            g_xlErrGettingData.val.err = xlerrGettingData;
            // Initialize the #N/A first-paint sentinel (loading_placeholder: "na").
            g_xlErrNA.xltype = xltypeErr;
            g_xlErrNA.val.err = xlerrNA;
            g_isUnloading = false;
            // Reset the single-shot teardown guard for symmetry with the
            // g_isUnloading reset above. Defense for the probe-unload-reuse
            // pattern: a probe FreeLibrary that ran DETACH and latched
            // g_teardownDone (if it ever did) followed by a real LoadLibrary
            // would otherwise start with the guard already true and silently
            // skip the real graceful teardown. Resetting here keeps ATTACH the
            // single place that restores both flags to their fresh-load state.
            g_teardownDone = false;
            // Reset the §23.6 Stage-4 deferred-teardown state for the same
            // probe-unload-reuse symmetry: the Phase-2 single-shot guard and the
            // RTD-handshake readiness signal must both start fresh on a real load.
            // (No watcher thread to reset — Phase 2 is now triggered on the STA from
            // RtdServer::ServerTerminate; §23.6 Stage-4 remediation.)
            xll::g_destructiveDone = false;
            xll::g_rtdServerTerminated.store(false, std::memory_order_release);
            break;
        case DLL_THREAD_ATTACH:
        case DLL_THREAD_DETACH:
            break;
        case DLL_PROCESS_DETACH:
            // DLL_PROCESS_DETACH is the UNIVERSAL destructive backstop. It fires
            // on a real quit's final unload AND on add-in-disable (FreeLibrary,
            // session continues) AND on a probe unload — but NEVER on a CANCELLED
            // quit (the DLL stays loaded there). It is therefore the safe place
            // for the minimal destructive signal, and — unlike xlAutoClose — it
            // can run without the cancelled-quit hazard. See AGENTS.md §20.
            //
            // We must NOT run the graceful drains here: per AGENTS.md §20.2
            // ("leak, don't crash") DETACH runs under the loader lock, where
            // blocking on a thread join can deadlock (a joined thread may need
            // the loader lock itself) and C++/SHM destructors are unsafe. So we
            // do the loader-lock-safe minimum: kernel calls (SetEvent,
            // CloseHandle) plus thread DETACH (not join). The graceful drains +
            // clean shutdown live in GracefulTeardownOnce(), driven from the COM
            // shutdown events which run on the STA thread (NOT the loader lock).
            //
            // ALWAYS-CLOSE hJob (orphan-prevent on PARTIAL teardown). This runs
            // BEFORE and OUTSIDE the !g_isUnloading guard below, unconditionally.
            // Rationale (MED, review 2026-06-13): GracefulTeardownOnce() sets
            // g_isUnloading=true EARLY, before it closes hJob near its end. If
            // that graceful path then aborted mid-way — e.g. the teardown hook's
            // SEH / XLL_SAFE_BLOCK swallowed a fault before reaching its
            // CloseHandle(hJob) — g_isUnloading would be true yet hJob still
            // open. The old `if (!g_isUnloading)`-gated close would then SKIP the
            // reap and the Go server (Job KILL_ON_JOB_CLOSE) would be ORPHANED
            // for the rest of the session on add-in disable. CloseHandle is a
            // kernel call (loader-lock-safe) and is null-checked + idempotent
            // (NULLs the field, and GracefulTeardownOnce already NULLs it on the
            // clean path), so doing it unconditionally here is safe and closes
            // the partial-teardown orphan window. We do NOT touch hProcess /
            // hShutdownEvent / g_phost here — see the §20.2 leak note below.
            if (g_procInfo.hJob) {
                CloseHandle(g_procInfo.hJob);
                g_procInfo.hJob = NULL;
            }

            // The !g_isUnloading guard: if GracefulTeardownOnce() already ran
            // (OnBeginShutdown / OnDisconnection on a real quit set g_isUnloading
            // and closed the handles), this block is a no-op — the heavy work is
            // already done. We only do the minimal signal+detach+kill when no
            // confirmed-shutdown signal preceded us (forced unload / add-in
            // disable without a COM add-in / probe).
            //
            // §20.2 leak note (intent — prevent loader-lock-unsafe "fixes"):
            // hProcess and hShutdownEvent are INTENTIONALLY LEAKED on this
            // forced-unload path. On a real process exit the OS reclaims them;
            // on add-in disable a one-session handle leak is accepted (§20.2).
            // Only hJob is closed (above) because it is the one whose closure has
            // a side effect we need: reaping the server via KILL_ON_JOB_CLOSE.
            // Do NOT add CloseHandle(hProcess/hShutdownEvent) or delete g_phost
            // here — closing/destructing under the loader lock risks the deadlock
            // §20.2 exists to avoid.
            if (!g_isUnloading) {
                 // Per AGENTS.md §20.2: under DLL_PROCESS_DETACH without a
                 // prior graceful teardown, the rule is "leak, don't crash" — we
                 // must minimize work and never block. The ordering below
                 // signals the threads FIRST (a kernel SetEvent is safe
                 // under the loader lock) and only then detaches, giving
                 // them a brief chance to observe g_isUnloading / the
                 // shutdown event before we orphan them.

                 // 1. Signal Unload
                 g_isUnloading = true;

                 // 2. Signal Shutdown Event first so MonitorThread can wake
                 //    and observe g_isUnloading before we detach it.
                 if (g_procInfo.hShutdownEvent) {
                     SetEvent(g_procInfo.hShutdownEvent);
                 }

                 // 3. (Server reap moved out: the Go server is reaped by the
                 //    ALWAYS-CLOSE CloseHandle(hJob) above, which now runs
                 //    unconditionally — including after a PARTIAL graceful
                 //    teardown that aborted before its own hJob close. We do NOT
                 //    delete g_phost here: an SHM/C++ destructor under the loader
                 //    lock is unsafe — leak it; the OS reclaims it on process
                 //    exit, and a one-session leak on add-in disable is
                 //    acceptable per §20.2.)

                 // 4. Detach Worker Thread
                 // Use ForceTerminateWorker to detach the thread so the C++ runtime
                 // doesn't call std::terminate() when the global std::thread is destructed.
                 xll::ForceTerminateWorker();

                 // 5. Detach Monitor Thread
                 // Detach monitor thread if running; it should check g_isUnloading and exit.
                 if (g_monitorThread.joinable()) {
                     try {
                         g_monitorThread.detach();
                     } catch (...) {
                         // Swallow any exception during detach - we're already in forced unload.
                     }
                 }
            }
            break;
        }
    XLL_SAFE_BLOCK_END(FALSE)
    return TRUE;
}

int xll::OnAutoClose() {
    XLL_SAFE_BLOCK_BEGIN
        // NON-DESTRUCTIVE. Excel calls xlAutoClose BEFORE the "Save changes? /
        // Cancel" dialog when the user quits or closes the last dirty workbook
        // (confirmed against Excel-DNA's "AutoClose and Excel shutdown" docs).
        // It is the ONLY callback that fires on a CANCELLED quit — so it must do
        // NOTHING irreversible, or a cancelled quit leaves the add-in a zombie
        // (server killed, g_phost deleted, g_isUnloading latched true, every UDF
        // returning #VALUE!, and no second xlAutoOpen to recover). See the design
        // at docs/superpowers/specs/2026-06-13-cancel-quit-teardown-design.md and
        // AGENTS.md §20.
        //
        // This function therefore MUST NOT: set g_isUnloading, SetEvent the
        // shutdown event, kill the server, CloseHandle(hJob), stop/join the
        // worker, run the §23.0 drains, or delete g_phost. On a cancelled quit
        // the host/worker/server all stay alive and the registered UDFs keep
        // working — exactly the desired behavior.
        //
        // The DESTRUCTIVE graceful teardown (drains + clean shutdown + handle
        // close) lives in GracefulTeardownOnce(), driven from the CONFIRMED-
        // shutdown COM events (OnBeginShutdown / OnDisconnection) which fire only
        // AFTER the cancel decision; the DETACH + Job hard-kill is the universal
        // backstop for the non-COM path. Both never run on a cancelled quit.
        //
        // EXPERIMENT-GATED FOLLOW-UP (design §5 / §8 decision 2): this design
        // assumes that after xlAutoClose + Cancel, Excel keeps this XLL's
        // functions REGISTERED. If a real-Excel experiment shows Excel
        // UNREGISTERS the XLL at xlAutoClose, the documented follow-up is to
        // re-register (re-run the xlfRegister loop) on the first CalculationEnded
        // after a cancelled xlAutoClose. That re-registration is NOT implemented
        // here (gated on the unrun experiment); do not add it without confirming.
        LogInfo("xlAutoClose called (non-destructive). XLL stays live until a "
                "confirmed shutdown signal (OnBeginShutdown / OnDisconnection / "
                "DLL_PROCESS_DETACH); a cancelled quit keeps all UDFs working.");
        return 1;
    XLL_SAFE_BLOCK_END(0)
}

void xll::GracefulTeardownOnce(bool isHostShutdown) {
    XLL_SAFE_BLOCK_BEGIN
        // Single-shot CAS: run the body EXACTLY ONCE regardless of which of
        // {OnBeginShutdown, OnDisconnection(HostShutdown|UserClosed), best-effort
        // DETACH} drives us first (AGENTS.md §20 / design §4). All callers run on
        // the STA thread (COM event delivery), which is COM/C++-safe and — unlike
        // DLL_PROCESS_DETACH — NOT the loader lock.
        //
        // §23.6 Stage 4 (close-time ghost fix, 2026-06-17): this function now has
        // TWO shapes keyed on isHostShutdown.
        //
        //  - NON-host-shutdown (add-in DISABLE / ext_dm_UserClosed, session
        //    continues): UNCHANGED behavior — run the destructive teardown
        //    SYNCHRONOUSLY (revoke the RTD class object, drain, delete g_phost,
        //    reap) right here. The session lives on, so there is no Excel RTD
        //    handshake to wait for.
        //
        //  - HOST SHUTDOWN (real Excel quit, ext_dm_HostShutdown / OnBeginShutdown):
        //    DEFERRED. Excel does NOT dispatch its RTD teardown COM calls
        //    (DisconnectData on every live topic, then ServerTerminate) until AFTER
        //    OnBeginShutdown returns — it serializes (proven, §23.6 Stage 3). If we
        //    did the destructive teardown synchronously here, g_phost would already
        //    be deleted and the server reaped by the time Excel issues DisconnectData,
        //    so MSG_RTD_DISCONNECT would go nowhere, ServerTerminate would never
        //    complete, and Excel ghosts (lingers windowless) holding live RTD topics.
        //    So Phase 1 (here) runs ONLY the fast, non-destructive prep — it must
        //    leave RTD fully usable (g_phost alive AND g_isUnloading==false, both
        //    required by xll_rtd.cpp::DisconnectData to actually send
        //    MSG_RTD_DISCONNECT) — and ARMS Phase 2. Phase 2 (RunDestructiveTeardown)
        //    runs the destructive sequence LATER, once Excel has finished its RTD
        //    handshake (ServerTerminate fired) or a bounded timeout elapses.
        bool expected = false;
        if (!g_teardownDone.compare_exchange_strong(expected, true)) {
            // Already entered (or a teardown is in progress and pumped us back in
            // re-entrantly — see the STA re-entrancy note below). PURE NO-OP.
            return;
        }

        LogInfo("GracefulTeardownOnce: confirmed shutdown — beginning teardown...");

        // Cancel any pending xlcOnTime-scheduled deferred-command runner (#3).
        // A late CalculationEnded (RTD-streaming recalc fires ~1/s) can arm an
        // xlcOnTime macro that Excel has not yet dispatched; left queued it can be
        // dispatched AFTER teardown. Run it FIRST (before the hook pumps the STA
        // loop) while the host is still reachable. It is a C-API command call,
        // valid from this STA macro/command context; self-guards (SEH + no-op when
        // nothing armed) and never throws. Safe to run on BOTH paths and BEFORE we
        // touch g_isUnloading (host-shutdown Phase 1 keeps g_isUnloading==false).
        xll::CancelDeferredRunner();

        // COM/ribbon/RTD destructive steps (ribbon disconnect, CoRevokeClassObject,
        // registry unregister, GDI+ down) live in the template TU and are invoked
        // through the registered hook so this TU stays decoupled. The hook runs on
        // the STA thread; ribbon loadImage callbacks arrive on this same thread, so
        // none can be in flight during it. The bool is revokeRtdClassObject:
        // !isHostShutdown — on a HOST SHUTDOWN we SKIP the RTD class-object revoke
        // (AGENTS.md §23.6) so Excel can START its RTD DisconnectData/ServerTerminate
        // handshake; on add-in disable (session continues) we still revoke.
        //
        // STA RE-ENTRANCY HARDENING (HIGH, review 2026-06-13): the hook's
        // SetRibbonConnected(false) PUMPS the STA message loop; Excel can re-enter
        // GracefulTeardownOnce() on THIS thread. The g_teardownDone CAS above turns
        // that into a no-op; the s_inHook guard additionally prevents the hook body
        // running twice on the same stack. Cleared via RAII on normal/exception
        // unwind; an async SEH fault may leave it set, harmless (the CAS already
        // prevents a second invocation this process generation).
        if (g_teardownHook) {
            static std::atomic<bool> s_inHook(false);
            bool hookExpected = false;
            if (s_inHook.compare_exchange_strong(hookExpected, true)) {
                struct HookGuard {
                    std::atomic<bool>& flag;
                    ~HookGuard() { flag.store(false, std::memory_order_release); }
                } hookGuard{s_inHook};
                g_teardownHook(!isHostShutdown);
            }
        }

        if (isHostShutdown) {
            // PHASE 1 (host shutdown): the COM hook above ran with the RTD revoke
            // SKIPPED, so Excel will (after we return) issue DisconnectData on each
            // live topic, then ServerTerminate. For those DisconnectData sends to
            // actually reach the server (xll_rtd.cpp::DisconnectData requires BOTH
            // g_phost alive AND g_isUnloading==false), we must NOT set g_isUnloading,
            // NOT StopWorker/Join, NOT drain, NOT delete g_phost, NOT reap here.
            // We RETURN FAST so Excel proceeds to its RTD teardown against a LIVE
            // g_phost.
            //
            // §23.6 Stage-4 REMEDIATION (2026-06-17): Phase 2 is NOT armed on an
            // off-STA watcher thread anymore. The destructive teardown is now
            // triggered DIRECTLY from RtdServer::ServerTerminate — which Excel calls
            // ON THE STA, AFTER all DisconnectData, once its RTD handshake completes.
            // That is the correct, COM-apartment-safe, naturally-serialized point:
            // same STA thread-class and same blocking profile the original
            // synchronous teardown had inside OnBeginShutdown, just correctly TIMED
            // (after Excel finished RTD teardown). This removes the prior
            // watcher-vs-DLL_PROCESS_DETACH races (off-STA std::terminate / unmap,
            // off-STA g_rtdServer UAF, off-STA m_callback apartment violation,
            // stale-watcher across probe-unload-reuse) flagged in the C++ review.
            //
            // BACKSTOP: if ServerTerminate never fires (no live RTD topics, or Excel
            // skips it for some topic shapes), DLL_PROCESS_DETACH is the universal
            // backstop — it closes hJob (reaps the server via KILL_ON_JOB_CLOSE) and
            // detaches threads per §20.2. We add NO watcher; a one-session g_phost
            // leak on that rare path is accepted (§20.2), and the server is still
            // reaped.
            LogInfo("GracefulTeardownOnce Phase 1 (host shutdown): COM hook done (RTD "
                    "class-object revoke skipped); returning fast so Excel can complete "
                    "its RTD DisconnectData/ServerTerminate handshake against a live "
                    "g_phost. Phase 2 (destructive teardown) will run from "
                    "RtdServer::ServerTerminate on the STA (AGENTS.md §23.6).");
            return;
        }

        // PHASE 2 (non-host-shutdown / add-in disable): no Excel RTD handshake to
        // wait for, so run the destructive teardown synchronously, in-line, on this
        // STA thread — exactly as before this Stage-4 split.
        xll::RunDestructiveTeardown();
    XLL_SAFE_BLOCK_END_VOID
}

// PHASE 2: the destructive teardown body. Reached from (a) RtdServer::ServerTerminate
// on a host shutdown — Excel calls it ON THE STA after all DisconnectData, once its
// RTD handshake completes — or (b) GracefulTeardownOnce synchronously on the
// non-host-shutdown / add-in-disable path (also the STA). Guarded by its own CAS
// (g_destructiveDone) so the body runs EXACTLY ONCE no matter which arrives first.
//
// THREAD CONTEXT: BOTH callers run on the STA thread (COM event delivery), and
// NEITHER runs under the loader lock — so thread joins, the §23.0 drains, and
// `delete g_phost` are all safe. This is the SAME thread-class and SAME blocking
// profile the original synchronous teardown had inside OnBeginShutdown; the Stage-4
// remediation only moves the host-shutdown invocation to the correctly-TIMED STA
// site (inside ServerTerminate, after Excel finished RTD teardown) instead of an
// off-STA watcher. The §23.0 ordering (delete g_phost ONLY AFTER the drains) is
// preserved. g_phost delete has no STA-only requirement either (DirectHost::Shutdown's
// sharedState.reset() invalidates slots independent of thread affinity — verified
// against shm DirectHost.h).
void xll::RunDestructiveTeardown() {
    XLL_SAFE_BLOCK_BEGIN
        bool expected = false;
        if (!g_destructiveDone.compare_exchange_strong(expected, true)) {
            // Already run (e.g. ServerTerminate kicked it AND the watcher timeout
            // path also fired, or the non-host-shutdown path raced a stray signal).
            return;
        }

        // Latch the unload flag. Set it (and signal the shutdown event) BEFORE the
        // drains so in-flight detached RTD/command threads observe g_isUnloading and
        // self-abort. On the host-shutdown path g_isUnloading was deliberately kept
        // FALSE through Phase 1 so Excel's DisconnectData could still send
        // MSG_RTD_DISCONNECT; by the time we reach here Excel's handshake is done
        // (ServerTerminate) or has timed out, so latching it now is correct.
        g_isUnloading = true;
        if (g_procInfo.hShutdownEvent) SetEvent(g_procInfo.hShutdownEvent);

        // Stop Worker
        xll::StopWorker();

        // Join threads before closing handles
        xll::JoinWorker();
        if (g_monitorThread.joinable()) g_monitorThread.join();

#ifdef XLL_RTD_ENABLED
        // Drain in-flight RTD ConnectData detached threads BEFORE deleting g_phost
        // (AGENTS.md §23.0). The 2 s cap is strictly larger than the ConnectData
        // thread's <=250 ms-per-attempt unload responsiveness; a timeout here is
        // should-never-happen and logged, not fatal.
        if (!WaitForRtdConnectDrain(2000)) {
            LogWarn("RTD ConnectData drain timed out unexpectedly (a Connect thread did not observe g_isUnloading within 2s)");
        }

        // Release OUR ref to Excel's IRTDUpdateEvent callback (breaks the
        // Excel<->RtdServer COM cycle). Idempotent + mutex-guarded so a prior
        // ServerTerminate (which also releases it) cannot double-free. Done AFTER
        // JoinWorker (no in-flight NotifyUpdate can race) and AFTER the ConnectData
        // drain. On the deferred host-shutdown path ServerTerminate has usually
        // already released it; this is the belt-and-suspenders / timeout-path cover.
        if (g_rtdServer) {
            g_rtdServer->ReleaseCallbackForTeardown();
        }
#endif

        // Drain in-flight SendCommandInvoke detached threads BEFORE deleting g_phost
        // (AGENTS.md §18.11 / §23.0). Same bounded-retry self-abort contract.
        if (!xll::ribbon::WaitForCommandDrain(2000)) {
            LogWarn("CommandInvoke drain timed out unexpectedly (a command thread did not observe g_isUnloading within 2s)");
        }

        // Cleanup SHM Host (Explicitly). The §23.0 drains above guarantee no
        // detached thread still touches g_phost.
        if (g_phost) {
            delete g_phost;
            g_phost = nullptr;
        }

        // Cleanup Process Handles. Closing hJob terminates the Go server via the
        // Job's KILL_ON_JOB_CLOSE (xll_launch.cpp) — a clean, explicit reap.
        if (g_procInfo.hProcess) {
            CloseHandle(g_procInfo.hProcess);
            g_procInfo.hProcess = NULL;
        }
        if (g_procInfo.hJob) {
            CloseHandle(g_procInfo.hJob);
            g_procInfo.hJob = NULL;
        }
        if (g_procInfo.hShutdownEvent) {
            CloseHandle(g_procInfo.hShutdownEvent);
            g_procInfo.hShutdownEvent = NULL;
        }
    XLL_SAFE_BLOCK_END_VOID
}

extern "C" __declspec(dllexport) int __stdcall xlAutoAdd(void) {
    XLL_SAFE_BLOCK_BEGIN
        return 1;
    XLL_SAFE_BLOCK_END(0)
}
