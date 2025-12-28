#include "xll_lifecycle.h"
#include "xll_log.h"
#include "xll_excel.h"
#include "xll_launch.h"
#include "xll_worker.h"
#include "xll_ipc.h"
#include "types/mem.h"
#include <cwchar>

using namespace xll;

// Global Handle
HINSTANCE g_hModule = NULL;
// Global Error Value
XLOPER12 g_xlErrValue;

// Unloading Flag
std::atomic<bool> g_isUnloading(false);

// Process Information for Server
ProcessInfo g_procInfo = { 0 };

std::thread g_monitorThread;

// Thread for monitoring server process
void MonitorThread(std::wstring logPath) {
    MonitorProcess(g_procInfo, logPath);
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
    // We construct a vector of ScopedXLOPER12.
    // Since ScopedXLOPER12 is move-only, we can use emplace_back.

    std::vector<ScopedXLOPER12> args;
    args.reserve(10 + argumentHelp.size());

    // 1. Module Name
    args.emplace_back(&xDLL);

    // 2. Procedure
    args.emplace_back(procedure);

    // 3. Type Text
    args.emplace_back(typeText);

    // 4. Function Text
    args.emplace_back(functionText);

    // 5. Argument Text
    args.emplace_back(argumentText);

    // 6. Macro Type
    args.emplace_back(macroType);

    // 7. Category
    args.emplace_back(category);

    // 8. Shortcut
    args.emplace_back(shortcut);

    // 9. Help Topic
    args.emplace_back(helpTopic);

    // 10. Function Description
    args.emplace_back(functionHelp);

    // 11+. Argument Descriptions
    for (const auto& help : argumentHelp) {
        args.emplace_back(help);
    }

    // Prepare pointers for Excel12v
    std::vector<LPXLOPER12> argPtrs;
    argPtrs.reserve(args.size());
    for (auto& arg : args) {
        argPtrs.push_back(arg);
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

                 // 1. Signal Unload
                 g_isUnloading = true;

                 // 2. Detach Worker Thread
                 // Use ForceTerminateWorker to detach the thread so the C++ runtime
                 // doesn't call std::terminate() when the global std::thread is destructed.
                 xll::ForceTerminateWorker();

                 // 3. Detach Monitor Thread
                 if (g_monitorThread.joinable()) {
                     g_monitorThread.detach();
                 }

                 // 4. Signal Shutdown Event (wakes up MonitorThread if it's still running detached)
                 if (g_procInfo.hShutdownEvent) {
                     SetEvent(g_procInfo.hShutdownEvent);
                 }
            }
            break;
        }
    XLL_SAFE_BLOCK_END(FALSE)
    return TRUE;
}

extern "C" __declspec(dllexport) int __stdcall xlAutoClose() {
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
