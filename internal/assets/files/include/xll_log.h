#pragma once

#include <string>

// Global path for native log file
extern std::string g_logPath;

// Log an error message to the configured log file
void LogError(const std::string& msg);

// Initialize logging (determines log path based on configuration and mode)
void InitLog(const std::wstring& configuredPath, const std::string& tempDirPattern, const std::string& projName, bool isSingleFile);
