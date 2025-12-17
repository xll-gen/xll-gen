#pragma once

#include <string>

// Safe Block Macros for Crash Handling
#ifdef _MSC_VER
    #include <windows.h>
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

// Forward declaration for the exception logger helper used in SEH
unsigned long LogException(unsigned long exceptionCode, void* exceptionPointers);


enum class LogLevel {
    DEBUG = 0,
    INFO = 1,
    WARN = 2,
    ERROR = 3,
    NONE = 4
};

// Global path for native log file
extern std::string g_logPath;
extern LogLevel g_logLevel;

// Log an error message to the configured log file
void LogError(const std::string& msg);

// Log an info message
void LogInfo(const std::string& msg);

// Log a debug message
#ifdef XLL_DEBUG_LOGGING
void LogDebug(const std::string& msg);
#else
#define LogDebug(msg) ((void)0)
#endif

// Initialize logging (determines log path based on configuration and mode)
void InitLog(const std::wstring& configuredPath, const std::string& level, const std::string& tempDirPattern, const std::string& projName, bool isSingleFile);
