#pragma once
#include <windows.h>
#include "types/xlcall.h"
#include <string>
#include <thread>
#include "xll_launch.h"
#include "shm/Logger.h"

// Global Handle
extern HINSTANCE g_hModule;
// Global Error Value
extern XLOPER12 g_xlErrValue;

// Process Information for Server
extern ProcessInfo g_procInfo;

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
