#include "xll_log.h"
#include "types/utility.h" // For StringToWString
#include <windows.h>
#include <fstream>
#include <chrono>
#include <ctime>
#include <algorithm>
#include <iostream>
#include <iomanip>
#include <vector>
#include <sstream>
#include <mutex>
#include <filesystem>

namespace xll {

static std::string g_logPath;
static LogLevel g_logLevel = LogLevel::INFO; // Default
static std::mutex g_logMutex;

// Helper to replace all occurrences of a substring
static void ReplaceString(std::wstring& str, const std::wstring& from, const std::wstring& to) {
    if(from.empty()) return;
    size_t start_pos = 0;
    while((start_pos = str.find(from, start_pos)) != std::wstring::npos) {
        str.replace(start_pos, from.length(), to);
        start_pos += to.length();
    }
}

static void WriteLog(const std::string& levelStr, const std::string& msg) {
    if (g_logPath.empty()) return;
    std::lock_guard<std::mutex> lock(g_logMutex);
    // Use filesystem::path for proper Unicode handling on Windows
    std::filesystem::path p = std::filesystem::u8path(g_logPath);
    std::ofstream logFile(p, std::ios_base::app | std::ios_base::out);
    logFile << std::unitbuf; // Force flush after every insertion
    if (logFile.is_open()) {
        auto now = std::chrono::system_clock::now();
        auto in_time_t = std::chrono::system_clock::to_time_t(now);
        auto ms = std::chrono::duration_cast<std::chrono::milliseconds>(now.time_since_epoch()) % 1000;

        struct tm buf;
        localtime_s(&buf, &in_time_t);

        char timeStr[64];
        // Format: YYYY-MM-DD HH:MM:SS.mmm
        std::strftime(timeStr, sizeof(timeStr), "%Y-%m-%d %H:%M:%S", &buf);

        logFile << "[" << timeStr << "." << std::setfill('0') << std::setw(3) << ms.count() << "] [" << levelStr << "] " << msg << std::endl;
    }
}

void LogError(const std::string& msg) {
    if (g_logLevel >= LogLevel::ERROR) {
        WriteLog("ERROR", msg);
    }
}

void LogWarn(const std::string& msg) {
    if (g_logLevel >= LogLevel::WARN) {
        WriteLog("WARN", msg);
    }
}

void LogInfo(const std::string& msg) {
    if (g_logLevel >= LogLevel::INFO) {
        WriteLog("INFO", msg);
    }
}

#ifdef XLL_DEBUG_LOGGING
void LogDebug(const std::string& msg) {
    if (g_logLevel >= LogLevel::DEBUG) {
        WriteLog("DEBUG", msg);
    }
}
#endif

// Helper to expand environment variables
std::wstring ExpandEnvVarsW(const std::wstring& pattern) {
    std::wstring p = pattern;
    size_t start_pos = 0;
    while((start_pos = p.find(L"${", start_pos)) != std::wstring::npos) {
        size_t end_pos = p.find(L"}", start_pos);
        if (end_pos != std::wstring::npos) {
            p.replace(end_pos, 1, L"%");
            p.replace(start_pos, 2, L"%");
            start_pos += 1;
        } else {
            break;
        }
    }

    DWORD reqSize = ExpandEnvironmentStringsW(p.c_str(), NULL, 0);
    if (reqSize == 0) return pattern;

    std::vector<wchar_t> buffer(reqSize);
    DWORD res = ExpandEnvironmentStringsW(p.c_str(), buffer.data(), reqSize);
    if (res == 0 || res > reqSize) return pattern;

    return std::wstring(buffer.data());
}

bool InitLog(const std::wstring& configuredPath, const std::string& level, const std::string& tempDirPattern, const std::string& projName, bool isSingleFile, std::string& outError) {
    outError = "";
    // Parse Level
    std::string l = level;
    std::transform(l.begin(), l.end(), l.begin(), ::tolower);
    if (l == "debug") g_logLevel = LogLevel::DEBUG;
    else if (l == "info") g_logLevel = LogLevel::INFO;
    else if (l == "warn") g_logLevel = LogLevel::WARN;
    else if (l == "error") g_logLevel = LogLevel::ERROR;
    else if (l == "none") g_logLevel = LogLevel::NONE;
    else g_logLevel = LogLevel::INFO; // Default

    // Determine path
    std::wstring path = configuredPath;

    std::wstring wProjName = StringToWString(projName);
    std::wstring logFileName = wProjName + L"_native.log";
    if (logFileName == L"_native.log") {
        logFileName = L"xll_native.log"; // Fallback if projName empty
    }

    if (path.empty()) {
        path = logFileName;
    }

    if (isSingleFile && (configuredPath.empty() || configuredPath == L"BIN_DIR" || configuredPath == L"TEMP_DIR")) {
        // Construct path in temp dir
        std::wstring wTempDirPattern = StringToWString(tempDirPattern);
        std::wstring tempDir = ExpandEnvVarsW(wTempDirPattern);

        // Remove trailing slash if any
        if (!tempDir.empty() && (tempDir.back() == L'\\' || tempDir.back() == L'/')) {
            tempDir.pop_back();
        }

        // Ensure directory exists
        try {
            std::filesystem::path dirPath(tempDir);
            std::filesystem::create_directories(dirPath);
        } catch (const std::exception& e) {
            outError = "Failed to create log directory '" + WideToUtf8(tempDir) + "': " + e.what();
            return false;
        } catch (...) {
            outError = "Failed to create log directory '" + WideToUtf8(tempDir) + "': Unknown error";
            return false;
        }

        path = tempDir + L"\\" + logFileName;
    }

    // Try to open file to verify permissions
    try {
        std::filesystem::path p = std::filesystem::u8path(WideToUtf8(path));
        std::ofstream logFile(p, std::ios_base::app);
        if (!logFile.is_open()) {
            outError = "Failed to open log file '" + WideToUtf8(path) + "' for writing.";
            return false;
        }
    } catch (const std::exception& e) {
        outError = "Exception opening log file: " + std::string(e.what());
        return false;
    }

    g_logPath = WideToUtf8(path);
    return true;
}

} // namespace xll
