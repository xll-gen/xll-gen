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

// Global path for native log file
extern std::string g_logPath;
extern LogLevel g_logLevel;

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

// Macros for SEH (Structured Exception Handling)
// Supported by MSVC and MinGW (with -fnon-call-exceptions / -fms-extensions)
#if defined(_MSC_VER) || defined(__MINGW32__)
    #define XLL_SAFE_BLOCK_BEGIN __try {
    #define XLL_SAFE_BLOCK_END(retVal) } __except(LogException(GetExceptionCode(), GetExceptionInformation())) { return retVal; }
    #define XLL_SAFE_BLOCK_END_VOID } __except(LogException(GetExceptionCode(), GetExceptionInformation())) { return; }
#else
    // Fallback for non-Windows/standard GCC (unlikely for XLL, but safe)
    // Note: Standard C++ try-catch does not catch SEH exceptions (like Access Violation).
    #define XLL_SAFE_BLOCK_BEGIN try {
    #define XLL_SAFE_BLOCK_END(retVal) } catch (...) { LogError("Unknown C++ Exception"); return retVal; }
    #define XLL_SAFE_BLOCK_END_VOID } catch (...) { LogError("Unknown C++ Exception"); return; }
#endif
