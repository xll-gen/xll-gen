#include "include/xll_log.h"
#include "include/xll_utility.h" // For StringToWString
#include <windows.h>
#include <fstream>
#include <sstream>
#include <iostream>
#include <iomanip>
#include <ctime>
#include <mutex>
#include <vector>

std::string g_logPath = "";
LogLevel g_logLevel = LogLevel::INFO;
std::mutex g_logMutex;

std::string GetTimestamp() {
    auto now = std::chrono::system_clock::now();
    auto in_time_t = std::chrono::system_clock::to_time_t(now);
    auto ms = std::chrono::duration_cast<std::chrono::milliseconds>(now.time_since_epoch()) % 1000;

    std::stringstream ss;
    struct tm buf;
    localtime_s(&buf, &in_time_t);
    ss << std::put_time(&buf, "%Y-%m-%d %H:%M:%S");
    ss << '.' << std::setfill('0') << std::setw(3) << ms.count();
    return ss.str();
}

void WriteLog(const std::string& levelStr, const std::string& msg) {
    std::lock_guard<std::mutex> lock(g_logMutex);
    if (g_logPath.empty()) return;

    std::ofstream ofs(g_logPath, std::ios::app);
    if (ofs.is_open()) {
        ofs << GetTimestamp() << " " << levelStr << " " << msg << std::endl;
    }
}

void LogError(const std::string& msg) {
    if (g_logLevel <= LogLevel::ERROR) {
        WriteLog("[ERROR]", msg);
    }
}

void LogInfo(const std::string& msg) {
    if (g_logLevel <= LogLevel::INFO) {
        WriteLog("[INFO] ", msg);
    }
}

#ifdef XLL_DEBUG_LOGGING
void LogDebug(const std::string& msg) {
    if (g_logLevel <= LogLevel::DEBUG) {
        WriteLog("[DEBUG]", msg);
    }
}
#endif

// Log exception info (SEH)
unsigned long LogException(unsigned long exceptionCode, void* exceptionPointers) {
    std::stringstream ss;
    ss << "FATAL ERROR: Exception Code: 0x" << std::hex << exceptionCode;

    // Attempt to get stack trace or more info if possible?
    // For now just the code.

    std::string msg = ss.str();
    LogError(msg);

    // Also show message box
    std::wstring wmsg(msg.begin(), msg.end());
    MessageBoxW(NULL, wmsg.c_str(), L"XLL Fatal Error", MB_OK | MB_ICONERROR);

    return EXCEPTION_EXECUTE_HANDLER;
    // This return value tells __except to execute the handler block.
    // In our macro, the handler block returns 0 or exits.
}

// Helper to expand environment variables
std::wstring ExpandEnvVarsW(const std::wstring& pattern) {
    // ExpandEnvironmentStringsW handles %VAR% natively
    // It doesn't handle ${VAR}, so we might need simple replacement if templates use ${}
    // However, xll_embed's ExpandEnvVars handled ${} to %.
    // Let's implement simple ${} -> % conversion for consistency.

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

void InitLog(const std::wstring& configuredPath, const std::string& level, const std::string& tempDirPattern, const std::string& projName, bool isSingleFile) {
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

    if (isSingleFile) {
        // Construct path in temp dir
        // tempDirPattern is the temp dir path
        // projName is project name
        std::wstring wTempDirPattern = StringToWString(tempDirPattern);
        std::wstring tempDir = ExpandEnvVarsW(wTempDirPattern);

        // Remove trailing slash if any
        if (!tempDir.empty() && (tempDir.back() == L'\\' || tempDir.back() == L'/')) {
            tempDir.pop_back();
        }

        path = tempDir + L"\\" + logFileName;
    }

    // Convert wstring path to string for ofstream (simple ANSI/UTF8 assumption)
    // In real world, use CreateFileW or wide streams. For now, simple conversion.
    std::string pathStr(path.begin(), path.end());
    g_logPath = pathStr;

    // Level
    if (level == "debug") g_logLevel = LogLevel::DEBUG;
    else if (level == "info") g_logLevel = LogLevel::INFO;
    else if (level == "warn") g_logLevel = LogLevel::WARN;
    else if (level == "error") g_logLevel = LogLevel::ERROR;
    else g_logLevel = LogLevel::INFO;
}
