#pragma once
#include <windows.h>
#include "types/xlcall.h"
#include "types/ScopedXLOPER12.h" // ScopedXLOPER12Result, used by RegisterOnTimeMacro below
#include <string>
#include <vector>
#include <thread>
#include <sstream>
#include <iomanip>
#include <atomic>
#include "xll_launch.h"
#include "shm/Logger.h"
#include "xll_log.h"

namespace xll {
#ifdef _MSC_VER
    inline DWORD LogException(DWORD code, PEXCEPTION_POINTERS pep) {
        (void)pep;
        std::stringstream ss;
        ss << "Caught SEH Exception: 0x" << std::hex << std::uppercase << code;
        LogError(ss.str());
        return EXCEPTION_EXECUTE_HANDLER;
    }
#endif

    // Helper to register a function safely (handles memory management internally)
    int RegisterFunction(
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
    );

    // RegisterOnTimeMacro registers `name` as an xlcOnTime-callable MACRO whose
    // Excel-visible procedure IS the exported C symbol of the same name. Both
    // schedulable macros the generated add-in has -- the calc-end deferred
    // command runner and the ribbon-connect retry tick -- used to spell this out
    // as two 18-line xlfRegister calls in xll_main.cpp.tmpl -- not byte-identical
    // of it carried a template variable, so it is one function here and the
    // template keeps only the WIRING: which macros this project registers.
    //
    // The four constants are NOT options:
    //   * TypeText "I" -- 2-byte signed int, matching the `short __stdcall`
    //     exports. macroType 2 ignores the returned value, but the type string
    //     still has to describe the signature (AGENTS.md §19.2).
    //   * macroType 2 -- a REGISTERED MACRO. Excel resolves an ON.TIME target by
    //     name against macro registrations; registered as a worksheet function
    //     (1) the symbol exists but every xlcOnTime schedule targeting it is
    //     rejected, which is a silent loss of the deferred work.
    //   * FunctionText == Procedure -- xlcOnTime targets the FunctionText, so
    //     the scheduled name, the registered name and the exported symbol are
    //     all the one literal the caller passed (the name accessors in
    //     xll_deferred_commands.h are that single source of truth).
    //   * empty ArgumentText / Category / Shortcut / help -- the macro takes no
    //     arguments and stays hidden from the user's function list.
    //
    // A failure is LOGGED, NOT FATAL: the add-in still loads, it just loses
    // whatever that macro deferred. Re-registering the same procedure name is
    // harmless, so this is idempotent across a probe-unload reload.
    //
    // `what` is the human-readable label used in the failure log line only.
    // Returns true when Excel accepted the registration.
    //
    // Defined inline so a translation unit can EXECUTE it against a stubbed
    // RegisterFunction without dragging in src/xll_lifecycle.cpp (which needs
    // shm, the worker and the ribbon add-in) -- see
    // internal/assets/ontime_macro_cpp_test.go.
    inline bool RegisterOnTimeMacro(const XLOPER12& xDLL, const wchar_t* name, const char* what) {
        ScopedXLOPER12Result xRegId;
        const int rc = RegisterFunction(
            xDLL,
            name,  // Procedure (the exported symbol)
            L"I",  // TypeText
            name,  // FunctionText (hidden; xlcOnTime targets it)
            L"",   // ArgumentText: none
            2,     // macroType=2 (registered macro; xlcOnTime-callable)
            L"",   // Category (unused)
            L"",   // Shortcut (none)
            L"",   // HelpTopic
            L"",   // FunctionHelp
            {},    // ArgumentHelp
            *xRegId
        );
        if (rc != xlretSuccess) {
            SAFE_LOG_ERROR(std::string("Failed to register ") + what +
                           " macro. Code: " + std::to_string(rc));
            return false;
        }
        return true;
    }

    // Unloading Flag. Meaning: "the DESTRUCTIVE teardown has begun — g_phost is
    // being or has been destroyed and the Go server reaped; touch NOTHING that
    // depends on them." Anything that must still reach the server while Excel is
    // shutting down (notably xll_rtd.cpp::DisconnectData, which needs to send
    // MSG_RTD_DISCONNECT for every live topic) is gated on THIS flag being false.
    extern std::atomic<bool> g_isUnloading;

    // Quiescing flag — the OTHER half of what g_isUnloading used to mean alone.
    //
    // Meaning: "a CONFIRMED teardown has begun: start no new background work and
    // self-abort anything in flight. g_phost and the Go server are STILL ALIVE."
    //
    // WHY THE SPLIT (2026-07-29 close-time use-after-unload; AGENTS.md §20.2/§23.6).
    // One flag was being asked to mean two things at once:
    //   (a) "stop background work / self-abort" — needed EARLY, in Phase 1, and
    //   (b) "the destructive teardown started, g_phost is going away" — which must
    //       NOT be true in Phase 1, because Excel issues its per-topic
    //       DisconnectData AFTER OnBeginShutdown returns and those sends require
    //       g_isUnloading==false (the §23.6 Stage-4 ghost fix).
    // Because (a) could not be latched without also latching (b), Phase 1 latched
    // NEITHER: the worker thread kept dispatching RTD updates, the hidden notify
    // window kept posting UpdateNotify into an Excel that was already shutting
    // down, and the whole destructive teardown (thread joins included) was deferred
    // into RtdServer::ServerTerminate — where it ran CONCURRENTLY with Excel
    // unmapping the XLL. Measured result: 100% crash on a window-close with live
    // streaming RTD topics (0xC0000005 in `<proj>.xll_unloaded`, faulting RIP the
    // instruction after WaitForSingleObject inside libwinpthread's pthread_join).
    //
    // Phase 1 now latches ONLY this flag, so background work stops while
    // DisconnectData keeps working. Set exclusively from GracefulTeardownOnce
    // (i.e. only from RibbonAddIn::OnBeginShutdown / OnDisconnection, which fire
    // ONLY on a CONFIRMED teardown and NEVER on a cancelled quit — so there is
    // nothing to un-latch there; the same call already does irreversible work
    // like the ribbon disconnect and the registry unregister). Cleared by
    // ResetLifecycleStateForFreshLoad(), which runs from DLL_PROCESS_ATTACH
    // (probe-unload-reuse symmetry, alongside g_isUnloading) AND from
    // PrepareForFreshLoad() - a PINNED image never gets a second ATTACH, so
    // xlAutoOpen has to be able to ask for the same reset (§20.2.1).
    extern std::atomic<bool> g_isQuiescing;

    // Convenience predicate for every background/self-scheduled work site:
    // "should I stop?" — true from Phase 1 onwards. Do NOT use this to gate a
    // send that must still reach the server during Excel's shutdown handshake
    // (DisconnectData); that one checks g_isUnloading alone, on purpose.
    inline bool TeardownStarted() {
        return g_isUnloading.load(std::memory_order_acquire) ||
               g_isQuiescing.load(std::memory_order_acquire);
    }

    // Process Information for Server
    extern ProcessInfo g_procInfo;

    extern std::thread g_monitorThread;

    // Publishes "the monitor thread has NOT exited" BEFORE the std::thread is
    // constructed. Mirrors what StartWorker does for g_workerExited, and closes the
    // same stale-flag window (review LOW #6): the flag starts true, so a teardown
    // landing between `g_monitorThread = std::thread(...)` and the thread's first
    // instruction would read a stale "exited" and take a REAL blocking join inside
    // a function whose contract is "never park". Call this immediately before
    // creating the thread.
    void MarkMonitorStarting();

    // True once MonitorThread has RETURNED. Same contract (and same reason) as
    // xll::WorkerExited() — see xll_worker.h: the graceful teardown must never
    // park in join(), so it waits on this flag first and only then joins.
    //
    // Diagnostics / test-facing, like WorkerExited(): the teardown uses the internal
    // bounded wait. Kept public so a harness can observe the flag directly.
    bool MonitorExited();

    // Thread for monitoring server process
    void MonitorThread(std::wstring logPath);
}

// Safe Block Macros for Crash Handling
#ifdef _MSC_VER
    // Log exception via SEH (defined in xll_log.cpp or just forward declared here if needed)
    // We use xll::LogException
    #define XLL_SAFE_BLOCK_BEGIN __try {
    #define XLL_SAFE_BLOCK_END(ret_val) } __except (xll::LogException(GetExceptionCode(), GetExceptionInformation()), EXCEPTION_EXECUTE_HANDLER) { return ret_val; }
    #define XLL_SAFE_BLOCK_END_VOID } __except (xll::LogException(GetExceptionCode(), GetExceptionInformation()), EXCEPTION_EXECUTE_HANDLER) { return; }
    #define XLL_SAFE_BLOCK_END_CONTINUE } __except (xll::LogException(GetExceptionCode(), GetExceptionInformation()), EXCEPTION_EXECUTE_HANDLER) { }

#else
    // For GCC/Clang (MinGW)
    #define XLL_SAFE_BLOCK_BEGIN try {
    #define XLL_SAFE_BLOCK_END(ret_val) } catch (...) { xll::LogError("Fatal Error: Unknown exception caught in safe block"); return ret_val; }
    #define XLL_SAFE_BLOCK_END_VOID } catch (...) { xll::LogError("Fatal Error: Unknown exception caught in safe block"); return; }
    #define XLL_SAFE_BLOCK_END_CONTINUE } catch (...) { xll::LogError("Fatal Error: Unknown exception caught in safe block"); }
#endif

// Macros for SEH
#define XLL_SAFE_BLOCK(block) __try { block } __except (EXCEPTION_EXECUTE_HANDLER) { }

// Global Handle
extern HINSTANCE g_hModule;
// Global Error Value
extern XLOPER12 g_xlErrValue;
// Global #GETTING_DATA sentinel — the first-paint placeholder rtd-once wrappers
// return on a cache miss (after wiring the RTD subscription via xlfRtd) so the
// cell reads as "fetching" instead of #N/A. Like g_xlErrValue it is a static
// XLOPER12 with no xlbitDLLFree set; Excel therefore never hands it to
// xlAutoFree12 (the SDK only invokes the free callback for DLL-owned results),
// so the static is safe to return repeatedly without any reclamation.
extern XLOPER12 g_xlErrGettingData;
// Global #N/A sentinel — the rtd-once first-paint placeholder selected by
// loading_placeholder: "na". Same static-XLOPER12 contract as g_xlErrValue and
// g_xlErrGettingData (no xlbitDLLFree).
extern XLOPER12 g_xlErrNA;

// Log Handler for SHM
#ifdef SHM_DEBUG
void LogHandler(shm::LogLevel level, const std::string& msg);
#endif

// Entry point
BOOL APIENTRY DllMain(HINSTANCE hModule, DWORD  ul_reason_for_call, LPVOID lpReserved);

namespace xll {
    // NON-DESTRUCTIVE xlAutoClose body: logs and returns 1. It must NOT tear
    // anything down, because Excel calls xlAutoClose BEFORE the Save/Cancel
    // prompt on quit, and a cancelled quit would otherwise leave the add-in a
    // zombie. See AGENTS.md §20 and the cancel-quit teardown design.
    int OnAutoClose();

    // The CONFIRMED-shutdown graceful teardown, guarded by an atomic CAS
    // (g_teardownDone) so it ENTERS exactly once. Driven from the CONFIRMED-
    // shutdown COM events (RibbonAddIn::OnBeginShutdown / OnDisconnection on both
    // ext_dm_HostShutdown and ext_dm_UserClosed). Runs on the STA thread (safe —
    // NOT the loader lock). Idempotent: a second/re-entrant call is a pure no-op
    // (the hook PUMPS the STA loop and Excel may re-enter this via OnDisconnection).
    //
    // DLL_PROCESS_DETACH MUST NOT call this. The joins the destructive phase
    // performs would run under the loader lock, where a joined thread that itself
    // needs the loader lock deadlocks (§20.2). DETACH instead does only the
    // loader-lock-safe minimum (SetEvent + always-close hJob + thread DETACH, no
    // join, no g_phost delete).
    //
    // §23.6 Stage 4 (close-time ghost fix — SHIPPED 2026-06-17): this function has
    // TWO shapes keyed on isHostShutdown.
    //
    //   isHostShutdown == false (add-in DISABLE / ext_dm_UserClosed, session
    //   continues): UNCHANGED — runs the destructive teardown SYNCHRONOUSLY here
    //   (revoke the RTD class object, drain, delete g_phost, reap).
    //
    //   isHostShutdown == true (REAL Excel quit — OnBeginShutdown, or
    //   OnDisconnection with ext_dm_HostShutdown): DEFERRED, in two phases. Excel
    //   does NOT dispatch its RTD teardown COM calls (DisconnectData per topic,
    //   then ServerTerminate) until AFTER OnBeginShutdown returns — it serializes.
    //   Phase 1 (here) runs ONLY the fast prep, in this order:
    //     1. PIN the XLL image (PinModuleToPreventUnmap) — Excel FreeLibrary's and
    //        UNMAPS the XLL ~80-100 ms after this function returns, while both it and
    //        we still have code/vtables in flight. MEASURED 2026-07-29; this is the
    //        root cause of the close-time use-after-unload crash. See §20.2.
    //     2. QUIESCE (BeginQuiesce), in THIS order: latch g_isQuiescing; SetEvent the
    //        private shutdown event (releases MonitorProcess only — the Go server never
    //        sees it); StopWorker; DRAIN the detached RTD-connect and command senders,
    //        CAPTURING both verdicts; BOUNDED-reap the worker/monitor; THEN destroy the
    //        hidden RTD notify window (after the reap, so no live worker can PostMessage
    //        into the destroy); THEN ReleaseCallbackForTeardown (whose documented
    //        precondition is "after the worker is reaped"); finally record
    //        g_backgroundThreadsReaped as the AND of ALL FOUR verdicts.
    //        Nothing here parks: joins happen only after the thread's own exit flag
    //        is observed, and a thread that misses its budget is detached (and, on the
    //        unpinned add-in-disable path, that detach pins — see PinModuleToPreventUnmap).
    //     3. CancelDeferredRunner + the COM hook with the RTD class-object revoke
    //        SKIPPED (so Excel can START its handshake), then RETURN FAST.
    //   Phase 1 deliberately leaves RTD USABLE — g_phost alive AND
    //   g_isUnloading==false, both required by xll_rtd.cpp::DisconnectData to
    //   actually send MSG_RTD_DISCONNECT. Phase 2 (RunDestructiveTeardown) runs the
    //   destructive sequence LATER, triggered DIRECTLY from RtdServer::ServerTerminate
    //   — which Excel calls ON THE STA after all DisconnectData, once its handshake
    //   completes (§23.6 Stage-4 remediation, 2026-06-17). This clears the windowless
    //   ghost: Excel completes its RTD topic teardown before the server is reaped and
    //   g_phost deleted. NOTHING ESSENTIAL DEPENDS ON ServerTerminate FIRING: if it
    //   never does, the DLL_PROCESS_DETACH backstop (§20.2) still reaps the server via
    //   hJob and g_phost/handles are simply leaked into process exit. The §23.0
    //   ordering is preserved — the drains run in Phase 1, strictly before Phase 2's
    //   `delete g_phost`, which is additionally gated on Phase 1 having actually
    //   reaped both background threads.
    void GracefulTeardownOnce(bool isHostShutdown = false);

    // PHASE 2 destructive teardown body: set g_isUnloading, release Excel's RTD
    // callback (idempotent), delete g_phost, CloseHandle of
    // hProcess/hJob/hShutdownEvent. Guarded by an internal CAS (g_destructiveDone)
    // so it runs EXACTLY ONCE across its two STA call sites:
    // RtdServer::ServerTerminate (host-shutdown deferred path) and
    // GracefulTeardownOnce itself (non-host-shutdown / add-in-disable path).
    // Declared here so the RTD server (include/rtd/server.h) can invoke it from
    // ServerTerminate on the STA, at the correctly-timed point after Excel finishes
    // its RTD handshake (§23.6 Stage-4 remediation, 2026-06-17). MUST run on the STA
    // (NOT the loader lock) — see the definition's THREAD CONTEXT note.
    //
    // IT MUST NOT PARK (2026-07-29). The thread stops/joins and the §23.0 drains
    // that used to live here MOVED to Phase 1 (BeginQuiesce): this body runs from
    // inside Excel's own RTD/COM shutdown, and a `join()` parked here used to return
    // into an image Excel had already unmapped — 0xC0000005 against
    // `<proj>.xll_unloaded`, 100% reproducible on a window-close with live streaming
    // RTD topics. Do NOT reintroduce a join / drain / message pump / Excel callback.
    void RunDestructiveTeardown();

    // Verdict from PrepareForFreshLoad().
    enum class FreshLoadVerdict {
        // Nothing was latched: an ordinary first load. Proceed.
        kCleanLoad,
        // A previous teardown HAD latched the lifecycle flags and they have now been
        // reset. Proceed; worth an INFO line because it means this process already
        // tore the add-in down once.
        kResetAfterTeardown,
        // A previous teardown left a background thread DETACHED rather than reaped,
        // so the flags CANNOT be safely reset. The caller must FAIL LOUDLY (refuse
        // to load) rather than continue.
        kUnrecoverable,
    };

    // MUST be called at the very top of xlAutoOpen, before anything is constructed.
    //
    // WHY IT EXISTS (review HIGH #2, 2026-07-29). The lifecycle flags used to be
    // reset in exactly one place, DLL_PROCESS_ATTACH, which was sound while every
    // unload really unmapped the image. The §20.2.1 image PIN broke that: after a
    // confirmed host shutdown the module is pinned, so a later
    // FreeLibrary/LoadLibrary pair just moves the reference count and DllMain is
    // never called again — the flags would stay latched for the life of the process.
    //
    // That is reachable, not theoretical: `Application.Quit()` from a COM automation
    // client that KEEPS its Application reference delivers OnBeginShutdown (so the
    // confirmed-shutdown teardown runs, and pins) while Excel does NOT exit. A
    // disable→re-enable after that would run xlAutoOpen with g_isQuiescing still
    // true: MonitorThread returns at once, WorkerLoop breaks out immediately, and
    // the add-in is silently half-dead for the rest of the session (no RTD updates,
    // no async results) while still holding the XLL file lock.
    //
    // Returns the verdict; see FreshLoadVerdict. The caller MUST honour
    // kUnrecoverable by failing the load.
    FreshLoadVerdict PrepareForFreshLoad();

    // Restores every lifecycle flag to its fresh-load state. Called from
    // DLL_PROCESS_ATTACH and from PrepareForFreshLoad(); not for general use.
    void ResetLifecycleStateForFreshLoad();

    // Registers the COM/ribbon/RTD destructive-teardown hook that
    // GracefulTeardownOnce() invokes (ribbon disconnect, CoRevokeClassObject,
    // registry unregister, GDI+ down). Called once from the generated xlAutoOpen
    // when a ribbon/command or RTD COM add-in exists; keeps this TU decoupled
    // from the template/ribbon/RTD symbols. Pass nullptr (or never call it) for
    // builds with no COM add-in.
    //
    // The hook receives revokeRtdClassObject: true => revoke the RTD class object
    // (add-in disable, session continues); false => SKIP the RTD revoke (host
    // shutdown — see GracefulTeardownOnce/§23.6). The ribbon revoke is unaffected.
    void SetGracefulTeardownHook(void (*hook)(bool revokeRtdClassObject));

    // Records that Excel has delivered RtdServer::ServerTerminate (its RTD
    // handshake completion). RtdServer::ServerTerminate calls this; defined in
    // xll_lifecycle.cpp. Retained for diagnosability / idempotence: Phase 2 is now
    // triggered DIRECTLY from inside ServerTerminate (on the STA, after it releases
    // m_callback) via RunDestructiveTeardown, rather than polled by a watcher
    // thread (§23.6 Stage-4 remediation, 2026-06-17).
    void SetRtdServerTerminated();

    // Reports, ONCE per session, that something called into the add-in AFTER the
    // destructive teardown completed — i.e. Excel is demonstrably still alive
    // (it just called us) but g_phost and the Go server are gone.
    //
    // WHY THIS EXISTS (backlog line 134/191, 2026-08-03). "Confirmed shutdown" is a
    // promise about the ADD-IN's shutdown, not the PROCESS's, and it CANNOT be
    // narrowed to "confirmed AND actually exiting": the only authoritative
    // discriminator in the process is `lpReserved` at DLL_PROCESS_DETACH, which
    // arrives strictly after every point at which the distinction could be acted on
    // (Phase 2 has already deleted g_phost and closed hJob, and DETACH runs under
    // the loader lock where nothing may be undone). Two reachable ways into this
    // state, both measured:
    //   * `Application.Quit()` from a COM client that KEEPS its `Application`
    //     reference: OnBeginShutdown is delivered, the teardown runs to completion,
    //     and EXCEL.EXE survives (8/8).
    //   * unticking the COM Add-ins box: OnDisconnection(ext_dm_UserClosed) drives
    //     the FULL destructive Phase 2 while the XLL stays LOADED and its UDFs stay
    //     REGISTERED. The user turned off a ribbon and lost the whole add-in.
    // Either way every UDF then hits its `g_phost == nullptr` guard and returns
    // #VALUE! for the rest of the session — SILENTLY. That silence is the defect
    // this function fixes; the recovery is to reload the add-in.
    //
    // CALL IT ONLY from a null-host guard on the STA (the generated UDF wrappers,
    // RtdServer::ConnectData's entry gate, SendCommandInvoke's entry gate). It logs
    // through LogTeardownWarn, so the same call-site restriction applies: never
    // from DllMain, never from a detached thread.
    void ReportPostTeardownUse(const char* site);

    // §23.6 host-shutdown teardown gate (remediation 2026-06-18). Returns true ONLY
    // when a CONFIRMED real host shutdown is in progress — i.e. GracefulTeardownOnce
    // ran its isHostShutdown Phase-1 branch (the unique real-quit signal). Reset to
    // false on DLL_PROCESS_ATTACH (probe-unload-reuse symmetry).
    //
    // RtdServer::ServerTerminate gates its RunDestructiveTeardown trigger on this:
    // Excel calls ServerTerminate not only at host shutdown but ALSO on an ordinary
    // workbook close once the live RTD topic count drops to zero (Application stays
    // alive). On that non-shutdown close the destructive teardown must NOT run — it
    // would kill the server mid-session and the next reopen would hit a dead server
    // (RPC 0x800706BA / AV). Only the armed (real-quit) case runs Phase 2.
    bool HostShutdownTeardownArmed();
}

// XLL Interface Functions
// xlAutoClose is now defined in xll_main.cpp to handle project-specific cleanup (like RTD)
// before calling xll::OnAutoClose()
extern "C" __declspec(dllexport) int __stdcall xlAutoAdd(void);
