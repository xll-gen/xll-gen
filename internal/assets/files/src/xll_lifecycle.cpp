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
#include <chrono>  // bounded thread-reap polls in BeginQuiesce
#include <thread>  // std::thread g_monitorThread member (reaped in Phase 1)
#include <atomic>  // g_destructiveDone / g_rtdServerTerminated CAS + signal
#ifdef XLL_RTD_ENABLED
#include "xll_rtd.h"
#include "xll_rtd_notify.h" // DestroyRtdNotifyWindow (STA-routed UpdateNotify, §0)
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

    // Quiescing Flag — see xll_lifecycle.h for the full rationale (the 2026-07-29
    // close-time use-after-unload fix). Latched by GracefulTeardownOnce's Phase 1,
    // BEFORE anything destructive, so background work stops while g_phost / the
    // server stay alive for Excel's RTD DisconnectData handshake.
    std::atomic<bool> g_isQuiescing(false);

    // Process Information for Server
    ProcessInfo g_procInfo = { 0 };

    std::thread g_monitorThread;

    // Budget for Phase 1's bounded, NON-PARKING reap of the worker + monitor
    // threads. Both normally return in microseconds (the worker is a poll loop;
    // the monitor is one WaitForMultipleObjects that Phase 1 signals), so this is
    // a generous should-never-hit ceiling, not an expected wait. Kept well under
    // any plausible host-shutdown patience so OnBeginShutdown still returns fast.
    static constexpr unsigned int kThreadReapBudgetMs = 500;

    // Set by MonitorThread as its last act (see MonitorExited in xll_lifecycle.h).
    static std::atomic<bool> g_monitorExited{true};
    bool MonitorExited() { return g_monitorExited.load(std::memory_order_acquire); }
    void MarkMonitorStarting() { g_monitorExited.store(false, std::memory_order_release); }

    // True only if BOTH background threads were observed to have RETURNED during
    // Phase 1's bounded reap. Phase 2 gates `delete g_phost` on it: if a thread
    // had to be DETACHED instead (it did not exit in budget), it may still touch
    // g_phost, so we LEAK the host object rather than risk a use-after-free —
    // the §20.2 "leak, don't crash" trade, applied where it is actually correct.
    static std::atomic<bool> g_backgroundThreadsReaped(false);

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

    // CONFIRMED-host-shutdown gate for RtdServer::ServerTerminate's destructive
    // teardown trigger (AGENTS.md §23.6, remediation 2026-06-18).
    //
    // WHY THIS EXISTS: the Stage-4 remediation wired RtdServer::ServerTerminate to
    // drive RunDestructiveTeardown directly, under the assumption "Excel calls
    // ServerTerminate ONLY at host shutdown (after OnBeginShutdown)". That
    // assumption is FALSE. Excel calls ServerTerminate WHENEVER the RTD server's
    // live topic count drops to zero — including on an ordinary workbook close
    // while the Excel Application stays alive (e.g. a COM-automation client holds
    // the Application ref, so OnBeginShutdown / GracefulTeardownOnce never fire).
    // On such a plain close Excel issues DisconnectData for each streaming topic,
    // then ServerTerminate — and if ServerTerminate unconditionally ran the
    // destructive teardown it would set g_isUnloading, Stop/Join the worker,
    // delete g_phost, and CloseHandle(hJob) (KILL_ON_JOB_CLOSE) — KILLING the Go
    // server while the XLL is still loaded and Excel is NOT quitting. The next
    // workbook reopen then hits a dead server / null g_phost → RPC 0x800706BA → AV.
    //
    // The UNIQUE signal that a real host shutdown is in progress (and therefore
    // that the subsequent ServerTerminate is the DEFERRED Phase-2 trigger we want)
    // is GracefulTeardownOnce's host-shutdown Phase-1 branch. We ARM this flag
    // there, before its fast return, and ServerTerminate gates RunDestructiveTeardown
    // on it: armed => real quit, run Phase 2; not armed => zero-topic blip on a
    // live host, leave g_phost / the server intact. Reset on DLL_PROCESS_ATTACH for
    // probe-unload-reuse symmetry (alongside g_destructiveDone / g_rtdServerTerminated).
    static std::atomic<bool> g_hostShutdownTeardownArmed(false);
    bool HostShutdownTeardownArmed() {
        return g_hostShutdownTeardownArmed.load(std::memory_order_acquire);
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
        g_monitorExited.store(false, std::memory_order_release);
        // If a teardown has already started, return immediately to avoid touching
        // global resources that may be freed during a forced unload.
        if (TeardownStarted()) {
            g_monitorExited.store(true, std::memory_order_release);
            return;
        }

        // Run the monitor; MonitorProcess honors the shutdown event, which Phase 1
        // signals (it is a private, unnamed, non-inherited event — xll_launch.cpp
        // — so signalling it affects ONLY this thread, never the Go server).
        MonitorProcess(g_procInfo, logPath);
        g_monitorExited.store(true, std::memory_order_release);
    }

    // Polls MonitorExited() until true or timeoutMs elapses.
    static bool WaitForMonitorExit(unsigned int timeoutMs) {
        using clock = std::chrono::steady_clock;
        auto deadline = clock::now() + std::chrono::milliseconds(timeoutMs);
        while (!g_monitorExited.load(std::memory_order_acquire)) {
            if (clock::now() >= deadline) return false;
            std::this_thread::sleep_for(std::chrono::milliseconds(1));
        }
        return true;
    }

    // Hold an extra module reference so the XLL image CANNOT be unmapped for the
    // rest of the process lifetime. Called ONLY from the CONFIRMED-host-shutdown
    // Phase 1 (a real Excel quit), never on the add-in-disable path.
    //
    // WHY (measured 2026-07-29): on a host shutdown with live streaming RTD topics
    // Excel calls FreeLibrary on the XLL ~80-100 ms after OnBeginShutdown returns
    // (DllMain DETACH with lpReserved==NULL) and the image is REALLY UNMAPPED —
    // while Excel still holds an IRtdServer* whose vtable lives in that image, and
    // while our own deferred Phase-2 teardown is still running. Both sides then
    // execute/read unmapped memory: 0xC0000005 against `<proj>.xll_unloaded` (our
    // side, parked in libwinpthread's pthread_join) and 0xC0000005 inside
    // EXCEL.EXE / mso20win32client.dll (Excel's side, dereferencing a vtable in
    // the hole). On the SAME close with no live RTD topics Excel never calls
    // FreeLibrary at all — DETACH arrives at process exit with lpReserved!=NULL,
    // nothing is unmapped, and the close is clean. This pin makes the RTD case
    // behave exactly like that clean case.
    //
    // This is also the plain COM contract: an in-process server must not be
    // unmapped while its objects are still referenced. Excel's XLL manager
    // FreeLibrary's without consulting DllCanUnloadNow (verified: our exported
    // DllCanUnloadNow is never called on this path), so the server has to hold the
    // reference itself.
    //
    // SCOPE, deliberately narrow - TWO call sites, both "we are about to be unmapped
    // with code of ours still live":
    //   1. A CONFIRMED host shutdown (GracefulTeardownOnce's isHostShutdown branch).
    //      The process is milliseconds from exiting, so leaking the image costs
    //      nothing and there is no session left to affect.
    //   2. The add-in-DISABLE path, ONLY when Phase 1's bounded reap had to DETACH a
    //      thread instead of reaping it (review MED #3). That path is otherwise NOT
    //      pinned - Excel unmaps right after OnDisconnection returns, DETACH runs, and
    //      a later re-enable gets a fresh DLL_PROCESS_ATTACH with its flag resets
    //      (probe-unload-reuse symmetry preserved, §20.2). But a detached thread there
    //      would be running INSIDE the image as it is unmapped, which is precisely the
    //      crash class this whole change fixes; pinning is the only way to keep
    //      "leak, don't crash" true. Cost: the image stays mapped for the session.
    //
    // In the normal disable case (both threads reaped, which is the measured case)
    // NOTHING is pinned and unload/re-enable behaves exactly as before. Dev-mode
    // "rebuild while Excel is open" is likewise unaffected there.
    //
    // GET_MODULE_HANDLE_EX_FLAG_PIN is used rather than a matched
    // LoadLibrary/FreeLibrary pair precisely BECAUSE it needs no matching release:
    // a self-FreeLibrary that happened to drop the last reference would unmap the
    // image under its own return address, which is the very bug being fixed.
    static void PinModuleToPreventUnmap() {
        HMODULE self = nullptr;
        if (GetModuleHandleExW(GET_MODULE_HANDLE_EX_FLAG_FROM_ADDRESS |
                               GET_MODULE_HANDLE_EX_FLAG_PIN,
                               reinterpret_cast<LPCWSTR>(&PinModuleToPreventUnmap),
                               &self) && self) {
            LogTeardown("Phase 1: XLL image PINNED for the remainder of this process "
                        "(host shutdown) — Excel's FreeLibrary can no longer unmap code "
                        "that Excel or we may still execute (AGENTS.md §20.2)");
        } else {
            LogTeardownWarn("Phase 1: GetModuleHandleExW(PIN) failed (err=" +
                        std::to_string((unsigned long)GetLastError()) +
                        ") — the close-time unmap hazard is NOT covered on this run");
        }
    }

    // PHASE 1 QUIESCE — the fix for the 2026-07-29 close-time use-after-unload
    // (AGENTS.md §20.2 / §23.6). Runs on the STA, INSIDE the confirmed-shutdown COM
    // callback (OnBeginShutdown / OnDisconnection), i.e. at a point where Excel is
    // demonstrably still calling into this image, and BEFORE Excel starts the RTD /
    // COM teardown during which it unmaps the XLL.
    //
    // It stops every piece of background machinery that could still be executing
    // XLL code (or calling back into a shutting-down Excel) while deliberately
    // leaving g_phost, the Go server and g_isUnloading==false intact so
    // xll_rtd.cpp::DisconnectData can still deliver MSG_RTD_DISCONNECT for every
    // live topic — the §23.6 Stage-4 ghost fix, unchanged.
    //
    // HARD RULE established by that crash, and its EXACT SCOPE (review MED #3):
    // on the HOST-SHUTDOWN path this function performs bounded kernel calls and
    // bounded polls but NEVER parks on another thread - join() is called ONLY after
    // the thread's own exit flag has been observed (so it returns immediately), and
    // a thread that misses its budget is DETACHED with the miss recorded so Phase 2
    // skips `delete g_phost` and the handle closes.
    //
    // OFF that path (add-in DISABLE / ext_dm_UserClosed) the OLD UNCONDITIONAL
    // BLOCKING JOIN IS KEPT, deliberately. The no-park rule was derived from the
    // host-shutdown timeline, where Excel unmaps the XLL CONCURRENTLY ~80-100 ms
    // after we return. On add-in disable Excel unmaps only AFTER OnDisconnection
    // RETURNS, and that path is NOT pinned - so a bare join() there is strictly
    // safe, while a budget miss + detach() would leave WorkerLoop / MonitorProcess
    // running into an imminent unmap, i.e. it would recreate on the disable path the
    // very defect this change fixes on the shutdown path. `hostShutdown` therefore
    // selects the reap strategy; it selects NOTHING else.
    static void BeginQuiesce(bool hostShutdown) {
        // (a) PUBLISH FIRST. Everything below (and every background site that
        //     checks TeardownStarted()) keys off this. Release-store pairs with the
        //     acquire-loads in the worker / RTD-notify / detached-send paths.
        g_isQuiescing.store(true, std::memory_order_release);

        // (b) Wake the monitor thread. Private, unnamed, non-inherited event
        //     (xll_launch.cpp) — signalling it affects ONLY MonitorProcess's
        //     WaitForMultipleObjects; the Go server never sees it.
        //
        //     It is a MANUAL-RESET event, which closes the one race the exit-flag
        //     reap in (g) would otherwise have: g_monitorExited starts true and is
        //     cleared by MonitorThread itself, so a quiesce that landed between
        //     `g_monitorThread = std::thread(...)` and the thread's first
        //     instruction would read a stale "exited" and go straight to join().
        //     Because the event stays signalled, that thread's very first wait
        //     returns immediately and the join completes in microseconds instead of
        //     parking. (In practice the gap is a few microseconds at xlAutoOpen,
        //     many seconds before any close.)
        if (g_procInfo.hShutdownEvent) SetEvent(g_procInfo.hShutdownEvent);

        // (c) Ask the worker to leave its loop (non-blocking flag store).
        xll::StopWorker();

        // (d)+(e) DRAIN the detached senders. Their verdicts are LOAD-BEARING, not
        //     just diagnostic: they feed g_backgroundThreadsReaped below, which is
        //     what allows Phase 2 to free g_phost and the two waitable handles. A
        //     timed-out drain means a detached RTD-connect or command thread may
        //     STILL be inside a Send against g_phost, so freeing it would be exactly
        //     the use-after-free the flag exists to prevent (review MED #2 - the
        //     previous version logged the timeout and then let the delete proceed).
        //     AGENTS.md §23.0 ordering is preserved: drains here, destruction in
        //     Phase 2.
        bool rtdDrained = true;
#ifdef XLL_RTD_ENABLED
        rtdDrained = WaitForRtdConnectDrain(2000);
        if (!rtdDrained) {
            LogTeardownWarn("Phase 1: RTD ConnectData drain timed out (a Connect thread "
                            "did not observe the quiesce flag within 2s) - g_phost and the "
                            "process handles will be LEAKED rather than freed");
        }
#endif

        const bool cmdDrained = xll::ribbon::WaitForCommandDrain(2000);
        if (!cmdDrained) {
            LogTeardownWarn("Phase 1: CommandInvoke drain timed out (a command thread "
                            "did not observe the quiesce flag within 2s) - g_phost and the "
                            "process handles will be LEAKED rather than freed");
        }

        // (f) Reap the two long-lived threads. The worker is a poll loop and the
        //     monitor is a single WaitForMultipleObjects we just signalled, so both
        //     normally return in well under a millisecond.
        //
        //     BOUNDED AND NON-PARKING ON BOTH PATHS - §20.2.1 rule 3 is
        //     unconditional. Observe the thread's OWN exit flag first, so the join
        //     that follows returns immediately; a thread that misses its budget is
        //     DETACHED instead of waited on.
        //
        //     Why not the historical unconditional blocking join on the add-in-disable
        //     path (review MED #3): MonitorProcess pops a MODAL MessageBoxW when it
        //     finds the Go server dead (xll_launch.cpp), so a bare
        //     g_monitorThread.join() there freezes Excel's STA until a human dismisses
        //     that dialog. Bounded + detach removes the hang; the detach's own hazard
        //     (running inside an image Excel unmaps right after OnDisconnection
        //     returns) is covered by pinning below, which is why that is not a
        //     regression of the bug this change fixes.
        const bool workerOut  = xll::WaitForWorkerExit(kThreadReapBudgetMs);
        const bool monitorOut = WaitForMonitorExit(kThreadReapBudgetMs);

        if (workerOut) {
            xll::JoinWorker();          // returns immediately: the thread is out
        } else {
            xll::ForceTerminateWorker(); // detach: leak, don't crash (§20.2)
            LogTeardownWarn("Phase 1: worker thread did not exit within budget - detached "
                            "(g_phost AND the process handles will be LEAKED rather than freed)");
        }
        if (monitorOut) {
            if (g_monitorThread.joinable()) g_monitorThread.join();
        } else if (g_monitorThread.joinable()) {
            try { g_monitorThread.detach(); } catch (...) {}
            LogTeardownWarn("Phase 1: monitor thread did not exit within budget - detached "
                            "(g_phost AND the process handles will be LEAKED rather than freed)");
        }

        // A DETACHED thread on the path that is NOT already pinned (add-in disable) is
        // the one case where "leak, don't crash" is not enough on its own: Excel unmaps
        // the XLL as soon as OnDisconnection returns, and that thread would still be
        // executing inside the image. Pin here, and ONLY here, so the normal disable
        // case (both threads reaped - the measured case) still unloads cleanly.
        if (!hostShutdown && !(workerOut && monitorOut)) {
            LogTeardownWarn("Phase 1 (add-in disable): a background thread had to be detached, so "
                            "the image is being PINNED to stop Excel unmapping code that is still "
                            "running. The XLL stays mapped for the rest of this session.");
            PinModuleToPreventUnmap();
        }

#ifdef XLL_RTD_ENABLED
        // (g) Destroy the hidden RTD notify window, on the STA that created it,
        //     while we are still mapped, and AFTER the reap above so no
        //     SignalRtdUpdate -> PostMessage from a still-live worker can race the
        //     destroy (review LOW #5; that is the precondition xll_rtd_notify.cpp
        //     documents). Two hazards it closes:
        //       * a WM_APP notify already queued on Excel's STA would otherwise be
        //         dispatched to a WndProc inside an UNMAPPED image (the exact
        //         hazard §20.3 documents for the removed ribbon retry TimerProc -
        //         "leak, don't crash" does NOT transfer to a raw code pointer the
        //         OS still dispatches), and
        //       * NotifyUpdate()/IRTDUpdateEvent::UpdateNotify would keep being
        //         pumped into an Excel that has already begun shutting down.
        //     Still well BEFORE CancelDeferredRunner and the STA-pumping COM hook,
        //     which is what the original placement was for. Idempotent.
        xll::DestroyRtdNotifyWindow();

        // (h) Release OUR ref to Excel's IRTDUpdateEvent callback, breaking the
        //     documented Excel<->RtdServer COM cycle (§23.6 Stage 2: necessary, if
        //     not sufficient, for a clean close). MUST come after the worker reap
        //     above — that is the documented precondition: no in-flight
        //     NotifyUpdate may race the release. Done HERE rather than only in
        //     Phase 2 because Phase 2 is reached from RtdServer::ServerTerminate,
        //     which is NOT guaranteed to fire (measured: on Excel 16.0.20228 a
        //     quiesced notify path means it never arrives). Idempotent + mutex
        //     guarded, so the Phase-2 call remains a harmless belt-and-braces.
        if (g_rtdServer) {
            g_rtdServer->ReleaseCallbackForTeardown();
        }
#endif

        // ALL FOUR verdicts, not just the two threads (review MED #2): the detached
        // RTD-connect and command senders touch g_phost too, so an undrained one is
        // just as much a reason to leak as an unreaped worker.
        const bool fullyReaped = workerOut && monitorOut && rtdDrained && cmdDrained;
        g_backgroundThreadsReaped.store(fullyReaped, std::memory_order_release);
        LogTeardown(std::string("Phase 1 quiesce complete (worker ") +
                    (workerOut ? "joined" : "DETACHED") + ", monitor " +
                    (monitorOut ? "joined" : "DETACHED") + ", rtd-connect drain " +
                    (rtdDrained ? "clean" : "TIMED OUT") + ", command drain " +
                    (cmdDrained ? "clean" : "TIMED OUT") + ", so Phase 2 will " +
                    (fullyReaped ? "free" : "LEAK") +
                    " g_phost/handles); g_phost and the Go server remain ALIVE for "
                    "Excel's DisconnectData handshake");
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
            // Every lifecycle flag back to its fresh-load state: the unload flag,
            // the quiesce flag, the two single-shot teardown guards, the RTD
            // handshake signal, the host-shutdown gate and the reap record. Kept in
            // ONE function because ATTACH is no longer its only caller - see
            // ResetLifecycleStateForFreshLoad and xll::PrepareForFreshLoad (a PINNED
            // image gets no second ATTACH, so xlAutoOpen has to be able to ask).
            xll::ResetLifecycleStateForFreshLoad();
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
        // PHASE 1 QUIESCE — FIRST, before ANYTHING else in this function.
        //
        // It must precede the teardown hook because the hook's SetRibbonConnected(false)
        // PUMPS the STA message loop, and a pumped WM_APP RTD-notify would otherwise
        // drive IRTDUpdateEvent::UpdateNotify into an Excel that has already begun
        // shutting down. It must also precede CancelDeferredRunner so no newly armed
        // background work slips in behind us. See BeginQuiesce() for the full
        // rationale (2026-07-29 close-time use-after-unload, AGENTS.md §20.2/§23.6).
        //
        // BeginQuiesce does NOT set g_isUnloading, does NOT touch g_phost and does
        // NOT reap the server — so the §23.6 Stage-4 invariant survives untouched:
        // Excel's per-topic DisconnectData still reaches a live server.
        //
        // On a CONFIRMED host shutdown, pin the image FIRST: from here on Excel may
        // FreeLibrary us at any moment, and everything below (plus everything Excel
        // still calls into) must not be executing out of a hole. See
        // PinModuleToPreventUnmap.
        if (isHostShutdown) PinModuleToPreventUnmap();
        BeginQuiesce(isHostShutdown);

        // Cancel any pending xlcOnTime-scheduled deferred-command runner (#3).
        // A late CalculationEnded (RTD-streaming recalc fires ~1/s) can arm an
        // xlcOnTime macro that Excel has not yet dispatched; left queued it can be
        // dispatched AFTER teardown. Run it FIRST (before the hook pumps the STA
        // loop) while the host is still reachable.
        //
        // EMPIRICAL CAVEAT (#3, 2026-07-24 — corrects an earlier assertion): this
        // call runs from OnBeginShutdown/OnDisconnection, which is a COM-event
        // context on the STA — NOT an Excel-dispatched macro/command context. Excel
        // does NOT permit command-class (xlc*) C-API calls there, so xlcOnTime is
        // REJECTED with xlretInvXlfn (rc=2) and NO de-queue occurs. (The SCHEDULE
        // side succeeds because it is issued from the xleventCalculationEnded
        // callback, which IS a valid command context.) This cancel is therefore a
        // no-op on the host-shutdown path; the actual guard against a leaked
        // dispatch is the runner's g_isUnloading/g_phost self-abort plus Excel
        // un-registering this XLL's macros on unload (and the §23.6 Stage-4
        // deferred-teardown split, which fixed the ghost independently of this
        // cancel). Kept as documented best-effort + diagnostic; self-guards
        // (SEH + no-op when nothing armed) and never throws. Safe on BOTH paths and
        // BEFORE g_isUnloading (host-shutdown Phase 1 keeps g_isUnloading==false).
        // See AGENTS.md §23.6 HIGH #2. KEEP: documented no-op diagnostic
        // (xll-cpp-reviewer approved 2026-07-24) — harmless under SEH/try-catch,
        // logs the xlret for diagnosis, and the schedule/cancel infrastructure
        // is reused by the planned OnTime-based ribbon connect retry.
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
            // ARM the host-shutdown teardown gate (AGENTS.md §23.6, remediation
            // 2026-06-18). This is the ONLY signal that a CONFIRMED real host
            // shutdown is in progress, so the ServerTerminate Excel issues after its
            // RTD handshake is the DEFERRED Phase-2 trigger — and must run the
            // destructive teardown. Without this gate, ServerTerminate also fires on
            // an ordinary workbook close (zero live topics) while Excel stays alive,
            // and an unconditional teardown there KILLS the server mid-session
            // (reopen → dead server → 0x800706BA). Set it BEFORE this fast return so
            // it is observably true by the time ServerTerminate runs on the STA.
            g_hostShutdownTeardownArmed.store(true, std::memory_order_release);
            LogInfo("GracefulTeardownOnce Phase 1 (host shutdown): quiesced (background "
                    "threads stopped/reaped, RTD notify window destroyed, drains done); "
                    "COM hook done (RTD class-object revoke skipped); armed "
                    "host-shutdown teardown gate; returning fast so Excel can complete "
                    "its RTD DisconnectData/ServerTerminate handshake against a live "
                    "g_phost. Phase 2 (minimal destructive remainder) will run from "
                    "RtdServer::ServerTerminate on the STA, or DLL_PROCESS_DETACH will "
                    "reap the server if it never fires (AGENTS.md §20.2/§23.6).");
            return;
        }

        // PHASE 2 (non-host-shutdown / add-in disable): no Excel RTD handshake to
        // wait for, so run the destructive teardown synchronously, in-line, on this
        // STA thread — exactly as before this Stage-4 split.
        xll::RunDestructiveTeardown();
    XLL_SAFE_BLOCK_END_VOID
}

// The one place that restores every lifecycle flag to its fresh-load state.
//
// Historically this lived inline in DLL_PROCESS_ATTACH, which was sufficient
// because every unload really unmapped the image and every reload therefore
// produced a fresh ATTACH. THE IMAGE PIN BROKE THAT (review HIGH #2): after a
// host-shutdown Phase 1 pins the module, a later FreeLibrary/LoadLibrary pair
// only moves the reference count - DllMain is NOT called again - so the flags
// would stay latched for the rest of the process. A subsequent xlAutoOpen would
// then build a brand-new g_phost and server while MonitorThread returned
// immediately at its TeardownStarted() check and WorkerLoop broke out of its
// loop at g_isQuiescing: an add-in that looks loaded but silently dispatches no
// async results and no RTD updates. So the block is a function now, called from
// ATTACH *and* from xll::PrepareForFreshLoad().
//
// It resets flags ONLY. It does not touch g_procInfo, g_phost, the threads or
// the RTD globals: on the path that calls it from a fresh load those were
// already cleaned by the completed teardown (and g_rtdServer self-manages -
// RtdServer's ctor/dtor own it).
void xll::ResetLifecycleStateForFreshLoad() {
    g_isUnloading = false;
    g_isQuiescing.store(false, std::memory_order_release);
    g_teardownDone = false;
    g_destructiveDone = false;
    g_rtdServerTerminated.store(false, std::memory_order_release);
    g_hostShutdownTeardownArmed.store(false, std::memory_order_release);
    g_backgroundThreadsReaped.store(false, std::memory_order_release);
}

// See xll_lifecycle.h. Called from xlAutoOpen BEFORE anything is built.
xll::FreshLoadVerdict xll::PrepareForFreshLoad() {
    const bool latched = g_teardownDone.load(std::memory_order_acquire) || TeardownStarted();
    if (!latched) {
        return FreshLoadVerdict::kCleanLoad;
    }

    // A teardown ran in THIS process generation and no DLL_PROCESS_ATTACH cleared
    // it (the image is pinned, or the load is a probe-unload-reuse). Resetting is
    // only safe if that teardown actually REAPED both background threads: if either
    // had to be detached it may still be running, and re-enabling background work
    // beside it would race a thread we no longer control. Refuse loudly instead -
    // silently coming back half-alive is the failure mode being fixed.
    //
    // LogTeardownWarn, not LogError: g_isUnloading may still be latched here and
    // every ordinary logger short-circuits on it.
    if (!g_backgroundThreadsReaped.load(std::memory_order_acquire)) {
        LogTeardownWarn("xlAutoOpen: a previous teardown in this process left a background "
                        "thread DETACHED (not reaped), so the lifecycle state cannot be "
                        "safely reset. REFUSING to load rather than coming back half-alive "
                        "(no RTD updates, no async results). Restart Excel.");
        return FreshLoadVerdict::kUnrecoverable;
    }

    ResetLifecycleStateForFreshLoad();
    LogTeardown("xlAutoOpen: a previous teardown had latched the lifecycle flags and the "
                "image is PINNED (no DLL_PROCESS_ATTACH to clear them) - state reset for "
                "this fresh load (AGENTS.md §20.2.1).");
    return FreshLoadVerdict::kResetAfterTeardown;
}

// PHASE 2: the destructive teardown body. Reached from (a) RtdServer::ServerTerminate
// on a host shutdown — Excel calls it ON THE STA after all DisconnectData, once its
// RTD handshake completes — or (b) GracefulTeardownOnce synchronously on the
// non-host-shutdown / add-in-disable path (also the STA). Guarded by its own CAS
// (g_destructiveDone) so the body runs EXACTLY ONCE no matter which arrives first.
//
// THREAD CONTEXT: BOTH callers run on the STA thread (COM event delivery) and
// NEITHER runs under the loader lock. But on the HOST-SHUTDOWN path this body runs
// from inside RtdServer::ServerTerminate — i.e. inside Excel's own COM/RTD shutdown,
// at a point where Excel has been MEASURED to FreeLibrary (lpReserved==NULL) and
// truly UNMAP the XLL concurrently (2026-07-29). So the rule for this function is:
//
//   NOTHING HERE MAY PARK. Bounded kernel calls and one destructor only. No
//   join, no drain, no message pump, no call back into Excel.
//
// CO-CHANGE ANCHOR (shm): the ONE destructor left here, `delete g_phost`, satisfies
// the no-park rule only because of a property of shm's DirectHost::Shutdown - its
// GuestCallWorker::Stop() joins `guestWorker` ONLY when `guestWorkerRunning` was true,
// and this XLL never calls DirectHost::Start() (the worker loop drives
// ProcessGuestCalls directly, xll_worker.cpp). If shm ever starts that thread
// implicitly, or if this XLL is changed to call Start(), that join lands HERE and this
// function parks again - which is the crash. Re-verify against shm/GuestCallWorker.h
// on any shm bump.
//
// Everything that parks or re-enters Excel now lives in Phase 1 (BeginQuiesce),
// which runs inside OnBeginShutdown — before Excel begins its RTD/COM teardown.
// The §23.0 ordering is preserved: the drains happen in Phase 1, strictly BEFORE
// this function's `delete g_phost`, and that delete is additionally gated on
// Phase 1 having actually reaped both background threads. g_phost delete has no
// STA-only requirement (DirectHost::Shutdown's sharedState.reset() invalidates
// slots independent of thread affinity — verified against shm DirectHost.h).

void xll::RunDestructiveTeardown() {
    XLL_SAFE_BLOCK_BEGIN
        bool expected = false;
        if (!g_destructiveDone.compare_exchange_strong(expected, true)) {
            // Already run (e.g. ServerTerminate kicked it AND the watcher timeout
            // path also fired, or the non-host-shutdown path raced a stray signal).
            LogTeardown("Phase 2: already done/in progress — CAS lost, no-op (tid=" +
                        std::to_string((unsigned long)GetCurrentThreadId()) + ")");
            return;
        }

        // Every line below uses LogTeardown, NOT LogInfo: g_isUnloading is latched
        // two statements down and LogInfo is suppressed from that point on. Before
        // this the whole destructive teardown was invisible in the log, and its
        // absence was misread as "Phase 2 never ran" (AGENTS.md §20.2 / §23.6).
        LogTeardown("Phase 2 (destructive teardown) ENTRY (tid=" +
                    std::to_string((unsigned long)GetCurrentThreadId()) + ")");

        // Latch the unload flag. On the host-shutdown path g_isUnloading was
        // deliberately kept FALSE through Phase 1 so Excel's DisconnectData could
        // still send MSG_RTD_DISCONNECT; by the time we reach here Excel has issued
        // every DisconnectData and then ServerTerminate, so latching it is correct.
        // (Phase 1 already latched g_isQuiescing, so background work stopped long
        // before this point — see BeginQuiesce.)
        g_isUnloading = true;
        if (g_procInfo.hShutdownEvent) SetEvent(g_procInfo.hShutdownEvent);

        // NOTE — WHAT DELIBERATELY IS *NOT* HERE ANY MORE (2026-07-29 fix):
        // StopWorker/JoinWorker, the monitor join, DestroyRtdNotifyWindow and the
        // two §23.0 drains all MOVED to Phase 1's BeginQuiesce(). They are the
        // operations that PARK or that call back into Excel, and this function runs
        // from inside RtdServer::ServerTerminate — i.e. inside Excel's own COM/RTD
        // shutdown, concurrently with Excel unmapping the XLL. A `join()` parked
        // here returns into UNMAPPED code (measured: 100% crash, 0xC0000005 in
        // `<proj>.xll_unloaded` at the instruction after WaitForSingleObject inside
        // libwinpthread's pthread_join). Everything left below is a bounded,
        // non-parking sequence of kernel calls plus one destructor. DO NOT
        // reintroduce a join / drain / message-pump / Excel callback here.
#ifdef XLL_RTD_ENABLED
        // Belt-and-suspenders: DestroyRtdNotifyWindow and the connect drain already
        // ran in Phase 1, and both are idempotent. Repeat the window destroy only —
        // it is a single kernel call and covers the (impossible-by-construction, but
        // cheap to cover) case of a Phase-2-only entry.
        xll::DestroyRtdNotifyWindow();

        // Release OUR ref to Excel's IRTDUpdateEvent callback (breaks the
        // Excel<->RtdServer COM cycle). Idempotent + mutex-guarded so a prior
        // ServerTerminate (which also releases it) cannot double-free. Safe here:
        // Phase 1 already stopped and reaped the worker, so no in-flight
        // NotifyUpdate can race the release.
        if (g_rtdServer) {
            g_rtdServer->ReleaseCallbackForTeardown();
        }
#endif

        // Cleanup SHM Host (Explicitly). Phase 1's drains + thread reap guarantee no
        // detached thread still touches g_phost — but ONLY if that reap actually
        // observed both threads exit. If either had to be DETACHED instead, LEAK the
        // host object rather than free memory a live thread may still read (§20.2
        // "leak, don't crash"; on a real quit the OS reclaims it moments later).
        if (g_phost && g_backgroundThreadsReaped.load(std::memory_order_acquire)) {
            delete g_phost;
            g_phost = nullptr;
        } else if (g_phost) {
            LogTeardownWarn("Phase 2: g_phost deliberately LEAKED - a background thread was "
                            "detached rather than reaped in Phase 1 (§20.2)");
        }
        LogTeardown("Phase 2: g_phost handled; closing process handles...");

        // Cleanup Process Handles.
        //
        // hJob is closed UNCONDITIONALLY: its closure is the side effect we need -
        // KILL_ON_JOB_CLOSE (xll_launch.cpp) reaps the Go server. Nothing waits on
        // it, so closing it under a detached thread is harmless.
        if (g_procInfo.hJob) {
            CloseHandle(g_procInfo.hJob);
            g_procInfo.hJob = NULL;
        }

        // hProcess and hShutdownEvent are gated on the SAME reap flag as
        // `delete g_phost` above (review MED #4). MonitorProcess parks in
        // WaitForMultipleObjects(2, { hProcess, hShutdownEvent }) - i.e. on exactly
        // these two handles. If Phase 1 had to DETACH the monitor instead of reaping
        // it, that thread is still parked on them; closing them under it lets the
        // handle VALUES be recycled by an unrelated object, whose signalling wakes
        // the monitor into the WAIT_OBJECT_0 branch and a GetExitCodeProcess on a
        // foreign handle -> a modal MessageBoxW("Server Crash") during shutdown.
        // Leaking two handles is the strictly better trade, and it is the same
        // treatment DllMain's DETACH backstop already documents for this pair
        // (one-session leak; the OS reclaims them at process exit, §20.2).
        if (g_backgroundThreadsReaped.load(std::memory_order_acquire)) {
            if (g_procInfo.hProcess) {
                CloseHandle(g_procInfo.hProcess);
                g_procInfo.hProcess = NULL;
            }
            if (g_procInfo.hShutdownEvent) {
                CloseHandle(g_procInfo.hShutdownEvent);
                g_procInfo.hShutdownEvent = NULL;
            }
        } else if (g_procInfo.hProcess || g_procInfo.hShutdownEvent) {
            LogTeardownWarn("Phase 2: hProcess/hShutdownEvent deliberately LEAKED - a background "
                            "thread was detached rather than reaped in Phase 1 and MonitorProcess "
                            "may still be parked on exactly those two handles (§20.2)");
        }
        LogTeardown("Phase 2 (destructive teardown) EXIT — server reaped, handles closed");
    XLL_SAFE_BLOCK_END_VOID
}

extern "C" __declspec(dllexport) int __stdcall xlAutoAdd(void) {
    XLL_SAFE_BLOCK_BEGIN
        return 1;
    XLL_SAFE_BLOCK_END(0)
}
