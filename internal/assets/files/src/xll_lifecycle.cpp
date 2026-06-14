#include "xll_lifecycle.h"
#include "xll_log.h"
#include "xll_excel.h"
#include "xll_launch.h"
#include "xll_worker.h"
#include "xll_ipc.h"
#include "types/mem.h"
#include "com/ribbon_addin.h" // WaitForCommandDrain (declared outside XLL_RIBBON_ENABLED)
#include <cwchar>
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
    static void (*g_teardownHook)() = nullptr;
    void SetGracefulTeardownHook(void (*hook)()) { g_teardownHook = hook; }

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

void xll::GracefulTeardownOnce() {
    XLL_SAFE_BLOCK_BEGIN
        // Single-shot CAS: run the heavy body EXACTLY ONCE regardless of which of
        // {OnBeginShutdown, OnDisconnection(HostShutdown|UserClosed), best-effort
        // DETACH} drives us first (AGENTS.md §20 / design §4). All callers run on
        // the STA thread (COM event delivery), which is COM/C++-safe and — unlike
        // DLL_PROCESS_DETACH — NOT the loader lock, so thread joins, the §23.0
        // drains, and g_phost delete are all safe here.
        bool expected = false;
        if (!g_teardownDone.compare_exchange_strong(expected, true)) {
            // Already torn down (or a teardown is in progress and pumped us back
            // in re-entrantly — see the STA re-entrancy note below). This is a
            // PURE NO-OP: we must NOT fall through to the joins / drains /
            // delete g_phost, which the winning caller owns and may be running
            // RIGHT NOW on this same thread further down the stack. Returning
            // here is what guarantees the re-entrant COM callback (a pumped-in
            // OnDisconnection(HostShutdown)) touches no half-torn state.
            return;
        }

        // Latch the unload flag. This is the ONLY place (besides the DETACH
        // backstop) that sets it — never xlAutoClose — so it is set ONLY on a
        // confirmed real teardown (design §4). Set it (and signal the shutdown
        // event below, before the hook) BEFORE invoking the hook so that
        // anything the hook PUMPS IN — see the STA re-entrancy note — observes
        // g_isUnloading and self-aborts instead of starting new SHM work.
        g_isUnloading = true;

        LogInfo("GracefulTeardownOnce: confirmed shutdown — tearing down XLL...");

        // Signal shutdown to monitor thread BEFORE the hook. The hook pumps the
        // STA loop (SetRibbonConnected disconnect), so detached command/RTD
        // threads that get scheduled during the pump must already see both
        // g_isUnloading=true (above) and the signalled shutdown event, so they
        // self-abort rather than start fresh Sends mid-teardown. This is a
        // loader-lock-safe kernel call and is idempotent with the re-signal in
        // the handle-close section below.
        if (g_procInfo.hShutdownEvent) SetEvent(g_procInfo.hShutdownEvent);

        // COM/ribbon/RTD destructive steps (ribbon disconnect, CoRevokeClassObject,
        // registry unregister, GDI+ down) live in the template TU and are invoked
        // through the registered hook so this TU stays decoupled. Running them here
        // (driven by OnBeginShutdown/OnDisconnection) means a CANCELLED quit never
        // disconnects the ribbon or revokes the class object. The hook runs on the
        // STA thread; ribbon loadImage callbacks arrive on this same thread, so
        // none can be in flight during it.
        //
        // STA RE-ENTRANCY HARDENING (HIGH, review 2026-06-13). The hook's
        // SetRibbonConnected(false) PUMPS the STA message loop. During that pump
        // Excel can deliver OnDisconnection(ext_dm_HostShutdown) and re-enter
        // GracefulTeardownOnce() on THIS SAME thread. The g_teardownDone CAS
        // above already turns that re-entrant call into a pure no-op (it returns
        // before any teardown work), so there is no double-teardown. We ADD a
        // dedicated re-entrancy guard around the hook itself: even though the CAS
        // makes a re-entrant GracefulTeardownOnce a no-op, the guard documents
        // and enforces that the hook body is never run twice on the same stack,
        // and makes the invariant local to the hook site (defense in depth if a
        // future caller reaches the hook without going through the CAS). The
        // guard is cleared via RAII on normal return and on C++ exception
        // unwind; under /EHsc an asynchronous SEH fault inside the hook may skip
        // the destructor and leave s_inHook set, but that is harmless because the
        // g_teardownDone CAS already prevents any second hook invocation in this
        // process generation.
        if (g_teardownHook) {
            static std::atomic<bool> s_inHook(false);
            bool hookExpected = false;
            if (s_inHook.compare_exchange_strong(hookExpected, true)) {
                struct HookGuard {
                    std::atomic<bool>& flag;
                    ~HookGuard() { flag.store(false, std::memory_order_release); }
                } hookGuard{s_inHook};
                g_teardownHook();
            }
            // else: a re-entrant pump reached here while the hook is mid-flight
            // further down this stack — skip the second invocation cleanly.
        }

        // Re-signal shutdown to monitor thread (idempotent with the pre-hook
        // SetEvent above; harmless if the event is already signalled, and a
        // belt-and-suspenders guarantee the monitor wakes even if it was created
        // between the pre-hook signal and here on some interleaving).
        if (g_procInfo.hShutdownEvent) SetEvent(g_procInfo.hShutdownEvent);

        // Stop Worker
        xll::StopWorker();

        // Join threads before closing handles
        xll::JoinWorker();
        if (g_monitorThread.joinable()) g_monitorThread.join();

#ifdef XLL_RTD_ENABLED
        // Drain in-flight RTD ConnectData detached threads BEFORE deleting
        // g_phost. Closes the UAF window documented in AGENTS.md §23.0. The
        // 2-second cap is now strictly larger than the ConnectData thread's
        // unload responsiveness: that thread sends via a bounded retry loop of
        // <=250 ms per-attempt timeouts and re-checks g_isUnloading between
        // attempts (see xll_rtd.cpp::ConnectData), so it returns within ~350 ms
        // worst case of g_isUnloading being set — well inside this cap. A timeout here is
        // therefore should-never-happen; it is still logged (not fatal) rather
        // than blocking teardown.
        if (!WaitForRtdConnectDrain(2000)) {
            LogWarn("RTD ConnectData drain timed out unexpectedly (a Connect thread did not observe g_isUnloading within 2s)");
        }
#endif

        // Drain in-flight SendCommandInvoke detached threads BEFORE deleting
        // g_phost. This drain runs after g_isUnloading=true: each command
        // thread re-checks the flag between its <=200 ms per-attempt Sends, so
        // it exits within ~one attempt (<~350 ms incl. shm's WaitEvent
        // quantum) — well inside the cap. Closes the command-path analogue of the RTD
        // ConnectData UAF window (AGENTS.md §18.11 / §23.0).
        if (!xll::ribbon::WaitForCommandDrain(2000)) {
            LogWarn("CommandInvoke drain timed out unexpectedly (a command thread did not observe g_isUnloading within 2s)");
        }

        // Cleanup SHM Host (Explicitly). Safe here (STA thread, not loader lock);
        // the §23.0 drains above guarantee no detached thread still touches
        // g_phost.
        if (g_phost) {
            delete g_phost;
            g_phost = nullptr;
        }

        // Cleanup Process Handles. Closing hJob terminates the Go server via the
        // Job's KILL_ON_JOB_CLOSE (xll_launch.cpp) — a clean, explicit reap on a
        // confirmed shutdown.
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
