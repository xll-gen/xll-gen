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

// Helper to expand environment variables (Wide)
static std::wstring ExpandEnvVarsW(const std::wstring& pattern) {
    std::wstring p = pattern;
    // Replace ${VAR} with %VAR%
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

    wchar_t buffer[MAX_PATH];
    DWORD res = ExpandEnvironmentStringsW(p.c_str(), buffer, MAX_PATH);
    if (res == 0 || res > MAX_PATH) {
        if (res > MAX_PATH) {
             std::vector<wchar_t> largeBuf(res);
             if (ExpandEnvironmentStringsW(p.c_str(), largeBuf.data(), res) != 0) {
                 return std::wstring(largeBuf.data());
             }
        }
        return pattern; // Fallback
    }
    return std::wstring(buffer);
}

// Wrapper to match template usage InitLogger
void InitLogger(const std::wstring& configuredPath, const std::string& level) {
    // For now, pass defaults for the extra params required by InitLog
    // Assuming standard XLL dir context
    InitLog(configuredPath, level, "${TEMP}", "xll", false);
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

    // Assume GetXllDir handles NULL or missing module handle gracefully by using GetModuleHandle(NULL)
    // In many DLL contexts, GetModuleHandle(NULL) returns the host EXE (Excel), which is NOT what we want.
    // We usually need the HMODULE of the DLL.
    // However, xll_launch/utility typically stores g_hModule or has a way to get it.
    // Given the context constraints, we'll assume GetXllDir() (no args or NULL) tries its best.
    // If we are called from xll_main, we might have resolved paths earlier.

    // NOTE: g_hModule is global in xll_main.cpp, but not extern-ed to here.
    // Ideally InitLog should take the resolved path.
    // xll_main.cpp resolves paths and calls InitLogger(logPath, level).
    // So if 'configuredPath' is already absolute, we are good.

    std::wstring xllDir = L"";
    // We only call GetXllDir if we need to resolve relative paths.

    // ... (rest of logic handles relative paths)

    // If xll_main passes an absolute path, we might not need xllDir.
    // But let's keep the existing logic structure.

    // To fix the build error "GetXllDir(NULL)", we check if GetXllDir is declared to take args.
    // xll_launch.h typically: std::wstring GetXllDir(HANDLE hModule);
    // Passing NULL is valid C++ (0).
    xllDir = GetXllDir(NULL);

    std::wstring wProjName = StringToWString(projName);
    if (wProjName.empty()) wProjName = L"xll";

    // 1. Substitute Internal Variables FIRST
    std::wstring wConfigured = configuredPath;
    ReplaceString(wConfigured, L"${XLL_DIR}", xllDir);
    ReplaceString(wConfigured, L"${XLL}", xllDir);

    // 2. Expand Environment Variables
    wConfigured = ExpandEnvVarsW(wConfigured);

    std::wstring wPath;

    // 3. Default if empty
    if (wConfigured.empty()) {
        wPath = xllDir + L"\\" + wProjName + L".log";
    } else {
        wPath = wConfigured;

        // 4. Handle Absolute Path
        bool isAbs = (wPath.find(L":") != std::wstring::npos || (wPath.size() > 1 && wPath[0] == L'\\' && wPath[1] == L'\\'));
        if (!isAbs) {
            wPath = xllDir + L"\\" + wPath;
        }

        // 5. Treat as Directory
        if (!wPath.empty() && wPath.back() != L'\\' && wPath.back() != L'/') {
            wPath += L"\\";
        }
        // Check if it looks like a directory (simplistic check: ends in separator or no extension?)
        // Actually the logic in the branch was:
        // if (lastDot == npos ... ) append filename.
        // But here we just append if it ends in slash?
        // Let's stick to the simpler check.
        // If it ends in separator, append filename.
        if (wPath.back() == L'\\') {
             wPath += wProjName + L".log";
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
