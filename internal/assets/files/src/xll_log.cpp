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
#include <atomic>

namespace xll {

static std::string g_logPath;
static LogLevel g_logLevel = LogLevel::INFO; // Default
// timed_mutex, not mutex: the teardown loggers must be able to bound how long
// they will wait for it. See WriteLogUnconditional.
static std::timed_mutex g_logMutex;

// Extern unloading flag (defined in xll_lifecycle.cpp)
extern std::atomic<bool> g_isUnloading;

// Helper to replace all occurrences of a substring
static void ReplaceString(std::wstring& str, const std::wstring& from, const std::wstring& to) {
    if(from.empty()) return;
    size_t start_pos = 0;
    while((start_pos = str.find(from, start_pos)) != std::wstring::npos) {
        str.replace(start_pos, from.length(), to);
        start_pos += to.length();
    }
}

// boundedWait: cap how long we will wait for g_logMutex, then DROP the line.
// Only the teardown loggers pass true -- see the comment on the lock below.
static void WriteLogUnconditional(const std::string& levelStr, const std::string& msg,
                                  bool boundedWait = false);

static void WriteLog(const std::string& levelStr, const std::string& msg) {
    // If unloading, avoid touching filesystem/logging resources.
    if (g_isUnloading) return;
    WriteLogUnconditional(levelStr, msg);
}

static void WriteLogUnconditional(const std::string& levelStr, const std::string& msg,
                                  bool boundedWait) {
    if (g_logPath.empty()) return;

    // WHY THE TEARDOWN PATH BOUNDS THIS WAIT.
    //
    // xll_log.h documents the teardown loggers as safe to call from Phase 2,
    // whose rule is "bounded kernel calls only, nothing may park". But this
    // function takes a lock and then does file I/O under it, and WriteLog's
    // g_isUnloading early-out is checked BEFORE the lock -- so a detached
    // thread that passed that check a moment earlier can be sitting inside this
    // critical section when Phase 2 calls LogTeardown. A plain lock there is an
    // unbounded park inside the one function that promised not to park.
    //
    // Bounded wait rather than try_lock: g_logMutex is normally held only for a
    // single append, so try_lock would drop teardown lines on ordinary
    // microsecond contention -- and those lines exist precisely because their
    // absence was misread once already (AGENTS.md §20.2). 50 ms keeps every
    // realistic case and still caps the pathological one. Dropping a log line
    // beats hanging Excel.
    std::unique_lock<std::timed_mutex> lock(g_logMutex, std::defer_lock);
    if (boundedWait) {
        if (!lock.try_lock_for(std::chrono::milliseconds(50))) return;
    } else {
        lock.lock();
    }
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
    if (g_isUnloading) return;
    if (g_logLevel >= LogLevel::ERROR) {
        WriteLog("ERROR", msg);
    }
}

void LogWarn(const std::string& msg) {
    if (g_isUnloading) return;
    if (g_logLevel >= LogLevel::WARN) {
        WriteLog("WARN", msg);
    }
}

void LogInfo(const std::string& msg) {
    if (g_isUnloading) return;
    if (g_logLevel >= LogLevel::INFO) {
        WriteLog("INFO", msg);
    }
}

// Teardown-path logger: deliberately NOT gated on g_isUnloading (see xll_log.h).
// Level-gated at INFO so `log.level: warn/error/none` still silences it.
void LogTeardown(const std::string& msg) {
    if (g_logLevel >= LogLevel::INFO) {
        WriteLogUnconditional("TEARDOWN", msg, /*boundedWait=*/true);
    }
}

// WARN-gated teardown logger - see xll_log.h. Survives `logging.level: warn`,
// which is where the teardown's failure lines have to be visible.
void LogTeardownWarn(const std::string& msg) {
    if (g_logLevel >= LogLevel::WARN) {
        WriteLogUnconditional("TEARDOWN-WARN", msg, /*boundedWait=*/true);
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
        // Fallback: construct the path in the extraction directory
        // <temp_dir>\<project>\ — the same directory ExtractEmbeddedExe uses
        // and where the launcher puts <proj>_go.log, so both logs stay together.
        std::wstring wTempDirPattern = StringToWString(tempDirPattern);
        std::wstring tempDir = ExpandEnvVarsW(wTempDirPattern);

        // Remove trailing slash if any
        if (!tempDir.empty() && (tempDir.back() == L'\\' || tempDir.back() == L'/')) {
            tempDir.pop_back();
        }
        if (!wProjName.empty()) {
            tempDir += L"\\" + wProjName;
        }

        path = tempDir + L"\\" + logFileName;
    }

    // Ensure the log directory exists (e.g. the singlefile extraction dir may
    // not have been created yet on a fresh machine).
    try {
        std::filesystem::path p = std::filesystem::u8path(WideToUtf8(path));
        if (p.has_parent_path()) {
            std::filesystem::create_directories(p.parent_path());
        }
    } catch (const std::exception& e) {
        outError = "Failed to create log directory for '" + WideToUtf8(path) + "': " + e.what();
        return false;
    } catch (...) {
        outError = "Failed to create log directory for '" + WideToUtf8(path) + "': Unknown error";
        return false;
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
