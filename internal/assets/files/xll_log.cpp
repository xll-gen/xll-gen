#include "include/xll_log.h"
#include "include/xll_utility.h"
#include "include/xll_embed.h"
#include <windows.h>
#include <fstream>
#include <chrono>
#include <ctime>

std::string g_logPath;

void LogError(const std::string& msg) {
    if (g_logPath.empty()) return;
    std::ofstream logFile(g_logPath, std::ios_base::app);
    if (logFile.is_open()) {
        auto now = std::chrono::system_clock::now();
        auto in_time_t = std::chrono::system_clock::to_time_t(now);
        struct tm buf;
        localtime_s(&buf, &in_time_t);
        char timeStr[32];
        std::strftime(timeStr, sizeof(timeStr), "%Y-%m-%d %H:%M:%S", &buf);

        logFile << "[" << timeStr << "] [ERROR] " << msg << std::endl;
    }
}

void InitLog(const std::wstring& configuredPath, const std::string& tempDirPattern, const std::string& projName, bool isSingleFile) {
    std::wstring xllDir = GetXllDir();
    std::wstring logDir = xllDir;

    if (isSingleFile && !tempDirPattern.empty()) {
        // In singlefile mode, we want logs in the temp directory
        std::string expandedTemp = embed::ExpandEnvVars(tempDirPattern);
        if (!expandedTemp.empty()) {
            std::string targetDir = expandedTemp + "\\" + projName;
            // Ensure dir exists
            CreateDirectoryA(targetDir.c_str(), NULL);
            logDir = StringToWString(targetDir);
        }
    }

    // Determine base name from configuredPath
    // logic mirrors template: {{fileBase .Logging.Path}}_native{{fileExt .Logging.Path}}
    // But since we are passing the full path, we can just append _native before extension

    std::wstring wPath = configuredPath;
    if (wPath.empty()) {
        // Fallback if not configured? The template usually guards this.
        // Assuming "xll.log" default if empty
        wPath = L"xll.log";
    }

    size_t lastDot = wPath.find_last_of(L".");
    std::wstring base, ext;
    if (lastDot != std::wstring::npos) {
        base = wPath.substr(0, lastDot);
        ext = wPath.substr(lastDot);
    } else {
        base = wPath;
        ext = L"";
    }

    std::wstring wLogName = base + L"_native" + ext;
    g_logPath = WideToUtf8(logDir + L"\\" + wLogName);
}
