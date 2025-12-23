#pragma once
#include <windows.h>
#include "types/xlcall.h"
#include <string>
#include <thread>
#include "xll_launch.h"
#include "shm/Logger.h"

// Macros for SEH
#ifdef _MSC_VER
    #define XLL_SAFE_BLOCK(block) __try { block } __except (EXCEPTION_EXECUTE_HANDLER) { }
#else
    // GCC/MinGW does not support MSVC-style __try/__except natively without extensions.
    // For compatibility, we execute the block without SEH protection.
    #define XLL_SAFE_BLOCK(block) { block }
#endif

// Global Handle
extern HINSTANCE g_hModule;
// Global Error Value
extern XLOPER12 g_xlErrValue;

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
