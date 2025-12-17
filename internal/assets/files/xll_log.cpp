#include "include/xll_log.h"
#include "include/xll_utility.h" // For StringToWString
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

std::string g_logPath;
LogLevel g_logLevel = LogLevel::INFO; // Default
std::mutex g_logMutex;

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

void LogInfo(const std::string& msg) {
    if (g_logLevel <= LogLevel::INFO) {
        WriteLog("INFO", msg);
    }
}

#ifdef XLL_DEBUG_LOGGING
void LogDebug(const std::string& msg) {
    if (g_logLevel <= LogLevel::DEBUG) {
        WriteLog("DEBUG", msg);
    }
}
#endif

// Log SEH Exception (Implementation of the helper used by macros)
unsigned long LogException(unsigned long exceptionCode, void* exceptionPointers) {
    std::stringstream ss;
    ss << "CRITICAL EXCEPTION DETECTED! Code: 0x" << std::hex << std::uppercase << exceptionCode;

    // Try to identify common codes
    if (exceptionCode == EXCEPTION_ACCESS_VIOLATION) ss << " (ACCESS_VIOLATION)";
    else if (exceptionCode == EXCEPTION_STACK_OVERFLOW) ss << " (STACK_OVERFLOW)";
    else if (exceptionCode == EXCEPTION_ILLEGAL_INSTRUCTION) ss << " (ILLEGAL_INSTRUCTION)";
    else if (exceptionCode == EXCEPTION_INT_DIVIDE_BY_ZERO) ss << " (INT_DIVIDE_BY_ZERO)";

    std::string msg = ss.str();

    // Force write to log
    WriteLog("CRASH", msg);

    // Show MessageBox
    std::wstring wMsg = StringToWString(msg);
    MessageBoxW(NULL, wMsg.c_str(), L"XLL Crash Detected", MB_ICONERROR | MB_OK | MB_TOPMOST);

    return EXCEPTION_EXECUTE_HANDLER;
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

    std::wstring wLogName = base + L"_native" + ext;
    g_logPath = WideToUtf8(wLogName);
}
