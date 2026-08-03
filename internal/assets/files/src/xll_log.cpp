#include "xll_log.h"
#include "types/utility.h" // For StringToWString / WideToUtf8 / GetXllDir
#include "xll_path.h"      // For ReplaceAll (the same helper the template used)
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

// THE NO-LOG-FILE SINK (2026-08-03).
//
// g_logPath is written in exactly ONE place: InitLog, reached only through
// InitNativeLogging, called only from xlAutoOpen. Every line logged while it is
// still empty used to be DISCARDED IN SILENCE, and there are two states where
// that is precisely the wrong outcome:
//
//   1. A COM activation in a session where xlAutoOpen NEVER RAN. The COM
//      Add-ins dialog row and the HKCU InprocServer32 deliberately survive an
//      add-in disable (see RibbonAddIn::OnConnection in src/ribbon_addin.cpp),
//      so Office can activate the ribbon class with no XLL loaded at all. The
//      refusal guard there logs at WARN — and its message was dropped EXACTLY
//      in the state the guard exists to cover, because no log file had ever
//      been opened. That is the defect this fallback fixes.
//   2. A FAILED InitLog. g_logPath stays empty on failure (by design — a path we
//      could not open must not be written to), so today the user gets one
//      message box and then a session-long blackout of every subsequent line.
//
// WHY HERE AND NOT AT THE ONE CALL SITE. This is the single choke point every
// logger already funnels through, so no call site has to know whether the file
// sink is up yet, and both states above are covered by one branch instead of a
// hand-placed OutputDebugStringW per early/late caller (the next one would be
// forgotten). It is not noisy either: the branch is reachable ONLY while
// g_logPath is empty, i.e. the few statements before InitLog in a normal load,
// after which it is inert forever. Every gate stays upstream and still applies —
// g_isUnloading in WriteLog and in the SAFE_LOG_* macros, and the g_logLevel
// gate in each logger.
//
// WHAT THAT LEVEL GATE MEANS IN EACH STATE — the gate applies, but in state (1)
// the CONFIGURED level has not been loaded yet, so do not read this as
// "`logging.level: none` silences the fallback":
//   * State (1) (no xlAutoOpen): InitLog never ran, so g_logLevel is still the
//     HARDCODED LogLevel::INFO default at the top of this file. A project
//     configured with `logging.level: none` therefore STILL emits the refusal
//     WARN here. That is deliberate — the config was never read, and the
//     alternative is the silent no-op this fallback exists to end — but it is a
//     real behaviour difference from a normally-loaded session.
//   * State (2) (failed InitLog): InitLog parses the level BEFORE it touches the
//     path, so the configured level is already in force and `logging.level: none`
//     does silence the fallback. TestFailedInitKeepsTheFallbackLive pins this.
//
// TEARDOWN SAFETY. The branch returns BEFORE the timed_mutex and before any file
// I/O, so it adds no lock and no disk access to the Phase 2 "bounded kernel
// calls only" rule (AGENTS.md §20.2). It also writes nothing to disk, so it
// cannot reopen the one-directory question of §18.12.
//
// …BUT OutputDebugStringW IS ONLY FREE WITH NOBODY LISTENING, and the teardown
// loggers do reach this branch (LogTeardown / LogTeardownWarn pass
// boundedWait=true, and TestTeardownLoggersFallBackToo pins that they still get
// a sink when there is no log file). With no debugger and no debug-output
// viewer running it goes nowhere and returns immediately. With a VIEWER running
// it takes the system-wide DBWinMutex and waits on the reader's ack event —
// classically bounded at 10 s, i.e. ~200x the 50 ms this function deliberately
// caps the teardown lock wait at below. So on the teardown path the fallback IS
// a wider bound than the file sink's, in the narrow state where all three of
// (no log file) + (a debug-output viewer running) + (forced unload) hold.
// Accepted rather than skipped for boundedWait callers: skipping would restore
// the session-long blackout for teardown lines after a failed InitLog, which is
// the exact silence §20.2 was mis-diagnosed from once already. Recorded here so
// the widened bound is a documented trade, not a surprise.
static void WriteDebugOutputFallback(const std::string& levelStr, const std::string& msg) {
    const std::string line = "[xll] [" + levelStr + "] " + msg + "\n";

    // UTF-8 -> UTF-16 by hand rather than via StringToWString: that helper THROWS
    // std::length_error on an oversized input, and a logger that can throw is not
    // usable from the paths this one exists for. A conversion that fails degrades
    // to the raw bytes rather than to nothing.
    const int needed = MultiByteToWideChar(CP_UTF8, 0, line.c_str(), -1, nullptr, 0);
    if (needed > 0) {
        std::wstring w(static_cast<size_t>(needed), L'\0');
        if (MultiByteToWideChar(CP_UTF8, 0, line.c_str(), -1, &w[0], needed) == needed) {
            OutputDebugStringW(w.c_str());
            return;
        }
    }
    OutputDebugStringA(line.c_str());
}

static void WriteLogUnconditional(const std::string& levelStr, const std::string& msg,
                                  bool boundedWait) {
    if (g_logPath.empty()) {
        // No log file yet (or ever) — see WriteDebugOutputFallback above.
        WriteDebugOutputFallback(levelStr, msg);
        return;
    }

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

// --- Native log bootstrap ---------------------------------------------------
// Relocated verbatim from internal/templates/xll_main.cpp.tmpl's xlAutoOpen
// preamble (2026-08-02); see xll_log.h for why, and AGENTS.md §18.12 for the
// four places the single-directory contract has to stay in step.

NativeLogPaths ResolveNativeLogPaths(const std::wstring& configuredDir,
                                     const std::string& projectName,
                                     const std::string& tempPattern,
                                     bool isSingleFile,
                                     const std::wstring& xllDir) {
    NativeLogPaths out;
    const std::wstring wProjName = StringToWString(projectName);

    out.binDir = xllDir;
    if (isSingleFile) {
        // Singlefile: ${BIN_DIR} is the extraction directory <temp_dir>\<project>
        // (where ExtractEmbeddedExe unpacks the Go server), so the default
        // logging.dir of ${BIN_DIR} keeps the exe and BOTH logs in one place.
        out.binDir = ExpandEnvVarsW(StringToWString(tempPattern));
        while (!out.binDir.empty() && (out.binDir.back() == L'\\' || out.binDir.back() == L'/')) out.binDir.pop_back();
        out.binDir += L"\\" + wProjName;
    }

    // Resolve logging.dir into ONE directory shared by both log files:
    // <dir>\<proj>_native.log (the XLL) and <dir>\<proj>_go.log (server stdout).
    std::wstring logDir = configuredDir;
    if (logDir == L"XLL_DIR") {
        logDir = xllDir;
    } else if (logDir == L"TEMP_DIR") {
        logDir = ExpandEnvVarsW(L"${TEMP}");
    } else {
        // Support placeholders like ${XLL_DIR}, ${BIN_DIR} and ${TEMP}
        ReplaceAll(logDir, L"${XLL_DIR}", xllDir);
        ReplaceAll(logDir, L"${BIN_DIR}", out.binDir);
        logDir = ExpandEnvVarsW(logDir);
    }
    if (logDir.empty()) logDir = out.binDir;
    while (!logDir.empty() && (logDir.back() == L'\\' || logDir.back() == L'/')) logDir.pop_back();

    out.dir = logDir;
    out.path = logDir + L"\\" + wProjName + L"_native.log";
    return out;
}

NativeLogPaths InitNativeLogging(const std::wstring& configuredDir,
                                 const std::string& level,
                                 const std::string& projectName,
                                 const std::string& tempPattern,
                                 bool isSingleFile) {
    NativeLogPaths paths = ResolveNativeLogPaths(configuredDir, projectName, tempPattern,
                                                 isSingleFile, GetXllDir());

    std::string logInitError;
    if (!InitLog(paths.path, level, tempPattern, projectName, isSingleFile, logInitError)) {
        // If logging fails to initialize, we show a message box but proceed.
        // Logging is critical for debugging but not for core functionality.
        MessageBoxA(NULL, ("Failed to initialize logging: " + logInitError).c_str(), "XLL Initialization Warning", MB_OK | MB_ICONWARNING);
    } else {
        SAFE_LOG_INFO("Logging Initialized Successfully. LogPath: " + WideToUtf8(paths.path));
    }
    return paths;
}

} // namespace xll
