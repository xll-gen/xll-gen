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

std::wstring ExpandEnvVarsW(const std::wstring& pattern);

void LogError(const std::string& msg);
void LogWarn(const std::string& msg);
void LogInfo(const std::string& msg);
#ifdef XLL_DEBUG_LOGGING
void LogDebug(const std::string& msg);
#else
inline void LogDebug(const std::string& msg) {}
#endif

} // namespace xll
