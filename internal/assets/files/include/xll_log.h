#pragma once
#include <string>
#include <windows.h> // Required for SEH macros (GetExceptionCode, etc.)

// Default Log Level
enum class LogLevel {
    NONE = 0,
    ERROR = 1,
    WARN = 2,
    INFO = 3,
    DEBUG = 4
};

// Initialize logging with a path, level, temp pattern, project name, and singlefile flag
void InitLog(const std::wstring& configuredPath, const std::string& level, const std::string& tempDirPattern, const std::string& projName, bool isSingleFile);

void LogError(const std::string& msg);
void LogInfo(const std::string& msg);
#ifdef XLL_DEBUG_LOGGING
void LogDebug(const std::string& msg);
#else
inline void LogDebug(const std::string& msg) {}
#endif

// SEH Exception Logger
unsigned long LogException(unsigned long code, void* exceptionPointers);

std::wstring GetXllDir();

// Safe Block Macros for Crash Handling
#ifdef _MSC_VER
    // Log exception via SEH (defined in xll_log.cpp or just forward declared here if needed)
    // We assume LogException is available where these macros are used, or we include windows.h
    // But xll_log.cpp implements it. We need a declaration to call it.

    // In xll_main.cpp and xll_worker.cpp we see:
    // LogException(GetExceptionCode(), GetExceptionInformation())

    // We can define the macro to use __try / __except
    #define XLL_SAFE_BLOCK_BEGIN __try {
    #define XLL_SAFE_BLOCK_END(ret_val) } __except (LogException(GetExceptionCode(), GetExceptionInformation()), EXCEPTION_EXECUTE_HANDLER) { return ret_val; }
    #define XLL_SAFE_BLOCK_END_VOID } __except (LogException(GetExceptionCode(), GetExceptionInformation()), EXCEPTION_EXECUTE_HANDLER) { return; }

#else
    // For GCC/Clang (MinGW), we use standard try-catch.
    // To catch crashes (segfaults), one must compile with -fnon-call-exceptions
    // and potentially ensure the signal is mapped to a C++ exception.
    // This is a "best effort" for GCC.
    #define XLL_SAFE_BLOCK_BEGIN try {
    #define XLL_SAFE_BLOCK_END(ret_val) } catch (...) { LogError("Fatal Error: Unknown exception caught in safe block"); return ret_val; }
    #define XLL_SAFE_BLOCK_END_VOID } catch (...) { LogError("Fatal Error: Unknown exception caught in safe block"); return; }
#endif
