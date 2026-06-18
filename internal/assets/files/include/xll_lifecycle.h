#pragma once
#include <windows.h>
#include "types/xlcall.h"
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

    // Unloading Flag
    extern std::atomic<bool> g_isUnloading;

    // Process Information for Server
    extern ProcessInfo g_procInfo;

    extern std::thread g_monitorThread;

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
    //   Phase 1 (here) runs ONLY the fast prep: the COM hook with the RTD
    //   class-object revoke SKIPPED (so Excel can START its handshake), then RETURNS
    //   FAST. It deliberately leaves RTD usable — g_phost alive AND
    //   g_isUnloading==false, both required by xll_rtd.cpp::DisconnectData to
    //   actually send MSG_RTD_DISCONNECT. Phase 2 (RunDestructiveTeardown) runs the
    //   destructive sequence LATER, triggered DIRECTLY from RtdServer::ServerTerminate
    //   — which Excel calls ON THE STA after all DisconnectData, once its handshake
    //   completes (§23.6 Stage-4 remediation, 2026-06-17). This clears the windowless
    //   ghost: Excel completes its RTD topic teardown before the server is reaped and
    //   g_phost deleted. If ServerTerminate never fires (no live topics), the
    //   DLL_PROCESS_DETACH backstop (§20.2) still reaps the server via hJob. The
    //   §23.0 ordering (delete g_phost ONLY AFTER the drains) is preserved in Phase 2.
    void GracefulTeardownOnce(bool isHostShutdown = false);

    // PHASE 2 destructive teardown body (set g_isUnloading, StopWorker/JoinWorker,
    // §23.0 drains, delete g_phost, CloseHandle of hProcess/hJob/hShutdownEvent).
    // Guarded by an internal CAS (g_destructiveDone) so it runs EXACTLY ONCE across
    // its two STA call sites: RtdServer::ServerTerminate (host-shutdown deferred
    // path) and GracefulTeardownOnce itself (non-host-shutdown / add-in-disable
    // path). Declared here so the RTD server (include/rtd/server.h) can invoke it
    // from ServerTerminate on the STA, at the correctly-timed point after Excel
    // finishes its RTD handshake (§23.6 Stage-4 remediation, 2026-06-17). MUST run
    // on the STA (NOT the loader lock) — see the definition's THREAD CONTEXT note.
    void RunDestructiveTeardown();

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
