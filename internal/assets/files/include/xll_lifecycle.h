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

    // The DESTRUCTIVE graceful teardown, guarded by an atomic CAS so it runs
    // EXACTLY ONCE. Driven from the CONFIRMED-shutdown COM events
    // (RibbonAddIn::OnBeginShutdown / OnDisconnection on both ext_dm_HostShutdown
    // and ext_dm_UserClosed). Runs on the STA thread (safe — NOT the loader
    // lock): sets g_isUnloading, runs the registered COM teardown hook, signals
    // the shutdown event, stops/joins the worker + monitor, runs the §23.0
    // RTD/command drains, deletes g_phost, and closes the process/job/event
    // handles. Idempotent: extra calls no-op (the CAS makes a second/re-entrant
    // call a pure no-op — important because the hook PUMPS the STA loop and Excel
    // may re-enter this on the same thread via OnDisconnection).
    //
    // DLL_PROCESS_DETACH MUST NOT call this. The joins it performs would run
    // under the loader lock, where a joined thread that itself needs the loader
    // lock deadlocks (§20.2). DETACH instead does only the loader-lock-safe
    // minimum (SetEvent + always-close hJob + thread DETACH, no join, no
    // g_phost delete); the graceful path here is reached ONLY from the STA COM
    // events.
    void GracefulTeardownOnce();

    // Registers the COM/ribbon/RTD destructive-teardown hook that
    // GracefulTeardownOnce() invokes (ribbon disconnect, CoRevokeClassObject,
    // registry unregister, GDI+ down). Called once from the generated xlAutoOpen
    // when a ribbon/command or RTD COM add-in exists; keeps this TU decoupled
    // from the template/ribbon/RTD symbols. Pass nullptr (or never call it) for
    // builds with no COM add-in.
    void SetGracefulTeardownHook(void (*hook)());
}

// XLL Interface Functions
// xlAutoClose is now defined in xll_main.cpp to handle project-specific cleanup (like RTD)
// before calling xll::OnAutoClose()
extern "C" __declspec(dllexport) int __stdcall xlAutoAdd(void);
