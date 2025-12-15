#pragma once

#include <string>

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

// Log a debug message
void LogDebug(const std::string& msg);

// Initialize logging (determines log path based on configuration and mode)
void InitLog(const std::wstring& configuredPath, const std::string& level, const std::string& tempDirPattern, const std::string& projName, bool isSingleFile);
