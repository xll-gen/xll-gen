#pragma once
#include <string>

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
void LogException(unsigned int code, void* exceptionPointers);

std::wstring GetXllDir();
