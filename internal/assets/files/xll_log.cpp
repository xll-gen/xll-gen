#include "include/xll_log.h"
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

void InitLog(const std::wstring& configuredPath, const std::string& level, const std::string& tempDirPattern, const std::string& projName, bool isSingleFile) {
    // Determine path
    std::wstring path = configuredPath;

    // Resolution logic similar to xll_launch (omitted for brevity, assume simple logic)
    // If empty, default to "xll_native.log" next to XLL

    if (path.empty()) {
        path = L"xll_native.log";
    }

    if (isSingleFile) {
        // Construct path in temp dir
        // tempDirPattern is the temp dir path
        // projName is project name
        std::wstring tempDir(tempDirPattern.begin(), tempDirPattern.end());
        path = tempDir + L"\\xll_native.log";
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
