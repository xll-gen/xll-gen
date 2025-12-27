#pragma once
#include <windows.h>
#include "types/xlcall.h"
#include <string>
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

    // Helper to create a deep copy string XLOPER12 (Safe for vectors/registration)
    XLOPER12 CreateDeepString(const std::wstring& s);
}

// Safe Block Macros for Crash Handling
#ifdef _MSC_VER
    // Log exception via SEH (defined in xll_log.cpp or just forward declared here if needed)
    // We use xll::LogException
    #define XLL_SAFE_BLOCK_BEGIN __try {
    #define XLL_SAFE_BLOCK_END(ret_val) } __except (xll::LogException(GetExceptionCode(), GetExceptionInformation()), EXCEPTION_EXECUTE_HANDLER) { return ret_val; }
    #define XLL_SAFE_BLOCK_END_VOID } __except (xll::LogException(GetExceptionCode(), GetExceptionInformation()), EXCEPTION_EXECUTE_HANDLER) { return; }

#else
    // For GCC/Clang (MinGW)
    #define XLL_SAFE_BLOCK_BEGIN try {
    #define XLL_SAFE_BLOCK_END(ret_val) } catch (...) { xll::LogError("Fatal Error: Unknown exception caught in safe block"); return ret_val; }
    #define XLL_SAFE_BLOCK_END_VOID } catch (...) { xll::LogError("Fatal Error: Unknown exception caught in safe block"); return; }
#endif

// Macros for SEH
#define XLL_SAFE_BLOCK(block) __try { block } __except (EXCEPTION_EXECUTE_HANDLER) { }

// Global Handle
extern HINSTANCE g_hModule;
// Global Error Value
extern XLOPER12 g_xlErrValue;

// Unloading Flag
extern std::atomic<bool> g_isUnloading;

// Process Information for Server
extern xll::ProcessInfo g_procInfo;

extern std::thread g_monitorThread;

// Thread for monitoring server process
void MonitorThread(std::wstring logPath);

// Log Handler for SHM
#ifdef SHM_DEBUG
void LogHandler(shm::LogLevel level, const std::string& msg);
#endif

// Entry point
BOOL APIENTRY DllMain(HINSTANCE hModule, DWORD  ul_reason_for_call, LPVOID lpReserved);

// XLL Interface Functions
extern "C" __declspec(dllexport) int __stdcall xlAutoClose();
extern "C" __declspec(dllexport) int __stdcall xlAutoAdd(void);
