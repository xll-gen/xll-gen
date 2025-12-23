#include "xll_lifecycle.h"
#include "xll_log.h"
#include "xll_launch.h"
#include "xll_worker.h"
#include "xll_ipc.h"
#include "types/mem.h"

using namespace xll;

// Global Handle
HINSTANCE g_hModule = NULL;
// Global Error Value
XLOPER12 g_xlErrValue;

// Process Information for Server
ProcessInfo g_procInfo = { 0 };

std::thread g_monitorThread;

// Thread for monitoring server process
void MonitorThread(std::wstring logPath) {
    MonitorProcess(g_procInfo, logPath);
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
            break;
        case DLL_THREAD_ATTACH:
        case DLL_THREAD_DETACH:
            break;
        case DLL_PROCESS_DETACH:
            // We intentionally do NOT perform cleanup here.
            // Excel may load and unload the DLL ("probe") without calling xlAutoOpen/xlAutoClose.
            // If xlAutoOpen was called, xlAutoClose will be called before unload.
            // Cleanup is handled exclusively in xlAutoClose.
            break;
        }
    XLL_SAFE_BLOCK_END(FALSE)
    return TRUE;
}

extern "C" __declspec(dllexport) int __stdcall xlAutoClose() {
    XLL_SAFE_BLOCK_BEGIN
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
