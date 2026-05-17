#include "xll_lifecycle.h"
#include "xll_log.h"
#include "xll_excel.h"
#include "xll_launch.h"
#include "xll_worker.h"
#include "xll_ipc.h"
#include "types/mem.h"
#include <cwchar>
#ifdef XLL_RTD_ENABLED
#include "xll_rtd.h"
#endif

using namespace xll;

// Global Handle
HINSTANCE g_hModule = NULL;
// Global Error Value
XLOPER12 g_xlErrValue;

namespace xll {
    // Unloading Flag
    std::atomic<bool> g_isUnloading(false);

    // Process Information for Server
    ProcessInfo g_procInfo = { 0 };

    std::thread g_monitorThread;

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
            g_isUnloading = false;
            break;
        case DLL_THREAD_ATTACH:
        case DLL_THREAD_DETACH:
            break;
        case DLL_PROCESS_DETACH:
            // Excel may load and unload the DLL ("probe") without calling xlAutoOpen/xlAutoClose.
            // Normally, cleanup is handled exclusively in xlAutoClose.
            // However, if xlAutoClose was skipped (e.g. forced unload), we must attempt to stop threads
            // to prevent 0xC0000005 crashes when code is unloaded while threads are running.
            if (!g_isUnloading) {
                 // Emergency Cleanup
                 // We cannot safely Join threads here due to Loader Lock, but we can signal them to stop.
                 // We assume that if g_isUnloading is false, xlAutoClose was NOT called.

                 // Per AGENTS.md §20.2: under DLL_PROCESS_DETACH without a
                 // prior xlAutoClose, the rule is "leak, don't crash" — we
                 // must minimize work and never block. The ordering below
                 // signals the threads FIRST (a kernel SetEvent is safe
                 // under the loader lock) and only then detaches, giving
                 // them a brief chance to observe g_isUnloading / the
                 // shutdown event before we orphan them. We do not add any
                 // new work; we only reorder the existing steps.

                 // 1. Signal Unload
                 g_isUnloading = true;

                 // 2. Signal Shutdown Event first so MonitorThread can wake
                 //    and observe g_isUnloading before we detach it.
                 if (g_procInfo.hShutdownEvent) {
                     SetEvent(g_procInfo.hShutdownEvent);
                 }

                 // 3. Detach Worker Thread
                 // Use ForceTerminateWorker to detach the thread so the C++ runtime
                 // doesn't call std::terminate() when the global std::thread is destructed.
                 xll::ForceTerminateWorker();

                 // 4. Detach Monitor Thread
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
        if (g_isUnloading) return 1; // Already called
        g_isUnloading = true;

        LogInfo("xlAutoClose called. Unloading XLL...");

        // Signal shutdown to monitor thread
        if (g_procInfo.hShutdownEvent) SetEvent(g_procInfo.hShutdownEvent);

        // Stop Worker
        xll::StopWorker();

        // Join threads before closing handles
        xll::JoinWorker();
        if (g_monitorThread.joinable()) g_monitorThread.join();

#ifdef XLL_RTD_ENABLED
        // Drain in-flight RTD ConnectData detached threads BEFORE deleting
        // g_phost. Closes the UAF window documented in AGENTS.md §23.0 (the
        // PARTIAL fix exposed the drain API but did not wire it into the
        // graceful-close path). 2-second cap matches typical SHM round-trip
        // budgets; a timeout here is logged but does not block — the
        // residual race is still narrower than the unpatched window.
        if (!WaitForRtdConnectDrain(2000)) {
            LogWarn("RTD ConnectData drain timed out; potential UAF window if a detached thread outlives g_phost");
        }
#endif

        // Cleanup SHM Host (Explicitly)
        if (g_phost) {
            delete g_phost;
            g_phost = nullptr;
        }

        // Cleanup Process Handles
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

        return 1;
    XLL_SAFE_BLOCK_END(0)
}

extern "C" __declspec(dllexport) int __stdcall xlAutoAdd(void) {
    XLL_SAFE_BLOCK_BEGIN
        return 1;
    XLL_SAFE_BLOCK_END(0)
}
