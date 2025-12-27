#include "xll_lifecycle.h"
#include "xll_log.h"
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

XLOPER12 xll::CreateDeepString(const std::wstring& s) {
    XLOPER12 x;
    x.xltype = xltypeStr | xlbitDLLFree;
    size_t len = s.length();
    if (len > 32767) len = 32767; // Truncate to XLOPER12 limit

    // Allocate buffer: 1 for length + len + 1 for null terminator
    x.val.str = new wchar_t[len + 2];

    // Set length at index 0
    x.val.str[0] = (wchar_t)len;

    // Copy string content
    if (len > 0) {
        wmemcpy(x.val.str + 1, s.c_str(), len);
    }

    // Null terminate
    x.val.str[len + 1] = L'\0';
    return x;
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
    std::vector<XLOPER12> args;
    // Reserve space: 10 fixed + arg help count
    args.reserve(10 + argumentHelp.size());

    // 1. Module Name (Copy xDLL)
    args.push_back(xDLL);

    // 2. Procedure
    args.push_back(CreateDeepString(procedure));

    // 3. Type Text
    args.push_back(CreateDeepString(typeText));

    // 4. Function Text
    args.push_back(CreateDeepString(functionText));

    // 5. Argument Text
    args.push_back(CreateDeepString(argumentText));

    // 6. Macro Type
    XLOPER12 xMacro;
    xMacro.xltype = xltypeInt;
    xMacro.val.w = macroType;
    args.push_back(xMacro);

    // 7. Category
    args.push_back(CreateDeepString(category));

    // 8. Shortcut
    args.push_back(CreateDeepString(shortcut));

    // 9. Help Topic
    args.push_back(CreateDeepString(helpTopic));

    // 10. Function Description
    args.push_back(CreateDeepString(functionHelp));

    // 11+. Argument Descriptions
    for (const auto& help : argumentHelp) {
        args.push_back(CreateDeepString(help));
    }

    // Prepare pointers for Excel12v
    std::vector<LPXLOPER12> argPtrs;
    argPtrs.reserve(args.size());
    for (auto& arg : args) {
        argPtrs.push_back(&arg);
    }

    int ret = Excel12v(xlfRegister, &xRegId, (int)argPtrs.size(), argPtrs.data());

    // Cleanup allocated strings (skip index 0 which is xDLL, and index 5 which is int)
    for (size_t i = 1; i < args.size(); ++i) {
        if (args[i].xltype & xltypeStr) {
            delete[] args[i].val.str;
        }
    }

    return ret;
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
