#include "include/xll_log.h"
#include "include/xll_utility.h"
#include "include/xll_embed.h"
#include <windows.h>
#include <fstream>
#include <chrono>
#include <ctime>
#include <algorithm>
#include <iostream>
#include <iomanip>

std::string g_logPath;
LogLevel g_logLevel = LogLevel::ERROR; // Default

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
    std::ofstream logFile(g_logPath, std::ios_base::app);
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
    if (g_logLevel <= LogLevel::ERROR) {
        WriteLog("ERROR", msg);
    }
}

void LogDebug(const std::string& msg) {
    if (g_logLevel <= LogLevel::DEBUG) {
        WriteLog("DEBUG", msg);
    }
}

void InitLog(const std::wstring& configuredPath, const std::string& level, const std::string& tempDirPattern, const std::string& projName, bool isSingleFile) {
    // Parse Level
    std::string lvl = level;
    std::transform(lvl.begin(), lvl.end(), lvl.begin(), ::tolower);
    if (lvl == "debug") g_logLevel = LogLevel::DEBUG;
    else if (lvl == "info") g_logLevel = LogLevel::INFO;
    else if (lvl == "warn") g_logLevel = LogLevel::WARN;
    else if (lvl == "error") g_logLevel = LogLevel::ERROR;
    else if (lvl == "none") g_logLevel = LogLevel::NONE;
    else g_logLevel = LogLevel::INFO; // Default to INFO if unspecified or unknown

    std::wstring xllDir = GetXllDir();
    std::wstring logDir = xllDir;

    // Default path if empty
    std::wstring wPath = configuredPath;
    if (wPath.empty()) {
        wPath = L"xll.log";
    }

    // Handle ${XLL} variable
    if (wPath.find(L"${XLL}") != std::wstring::npos) {
        ReplaceString(wPath, L"${XLL}", xllDir);
    } else {
        // Legacy logic
        if (isSingleFile && !tempDirPattern.empty()) {
            std::string expandedTemp = embed::ExpandEnvVars(tempDirPattern);
            if (!expandedTemp.empty()) {
                std::string targetDir = expandedTemp + "\\" + projName;
                CreateDirectoryA(targetDir.c_str(), NULL);
                logDir = StringToWString(targetDir);
            }
        }

        bool isAbs = (wPath.find(L":") != std::wstring::npos || (wPath.size() > 1 && wPath[0] == L'\\' && wPath[1] == L'\\'));
        if (!isAbs) {
            wPath = logDir + L"\\" + wPath;
        }
    }

    // Inject _native
    size_t lastDot = wPath.find_last_of(L".");
    size_t lastSlash = wPath.find_last_of(L"\\");

    std::wstring base, ext;
    if (lastDot != std::wstring::npos && (lastSlash == std::wstring::npos || lastDot > lastSlash)) {
        base = wPath.substr(0, lastDot);
        ext = wPath.substr(lastDot);
    } else {
        base = wPath;
        ext = L"";
    }

    std::wstring wLogName = base + L"_native" + ext;
    g_logPath = WideToUtf8(wLogName);
}
