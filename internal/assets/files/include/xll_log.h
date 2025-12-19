#pragma once
#include <string>
#include <windows.h> // Required for SEH macros (GetExceptionCode, etc.)

namespace xll {

// Default Log Level
enum class LogLevel {
    NONE = 0,
    ERROR = 1,
    WARN = 2,
    INFO = 3,
    DEBUG = 4
};

// Initialize logging with a path, level, temp pattern, project name, and singlefile flag
// Returns true on success, false on failure (with error message in outError)
bool InitLog(const std::wstring& configuredPath, const std::string& level, const std::string& tempDirPattern, const std::string& projName, bool isSingleFile, std::string& outError);

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

} // namespace xll

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
