#pragma once
#include <string>
#include <windows.h> // Required for SEH macros (GetExceptionCode, etc.)

namespace xll {

// Default Log Level
enum class LogLevel {
    NONE = 0,
    ERROR = 1,
    WARN = 2,
    INFO = 3,
    DEBUG = 4
};

// Initialize logging with a path, level, temp pattern, project name, and singlefile flag
// Returns true on success, false on failure (with error message in outError)
bool InitLog(const std::wstring& configuredPath, const std::string& level, const std::string& tempDirPattern, const std::string& projName, bool isSingleFile, std::string& outError);

std::wstring ExpandEnvVarsW(const std::wstring& pattern);

// --- Native log bootstrap ---------------------------------------------------
//
// The whole xlAutoOpen logging preamble used to be inline in
// internal/templates/xll_main.cpp.tmpl: the ${BIN_DIR} derivation, the
// logging.dir resolution, the log file name, the InitLog call and its
// failure MessageBox. None of that LOGIC was generated — only the four
// xll.yaml values it consumes — so every generated project received a
// byte-identical re-emitted copy that nothing but a golden-string grep could
// test. It lives here now, and the arguments below are the whole contract with
// the generated TU. (Standing project direction: keep generated templates
// minimal, prefer static code. Same move as com/ribbon_connect.h and
// com/scratch_book.h — AGENTS.md §18.11.1.)

// The ONE-DIRECTORY CONTRACT (AGENTS.md §18.12). logging.dir resolves to a
// single directory that holds BOTH log files: <proj>_native.log (this XLL) and
// <proj>_go.log (the launched Go server's redirected stdout, written by
// xll_launch.cpp from LaunchConfig::logDir). Do NOT reintroduce a per-side
// default — the Go log hardcoded to the launch cwd while the native log honored
// logging.dir was the original split-log bug in singlefile mode.
struct NativeLogPaths {
    // ${BIN_DIR}: the XLL's own directory, EXCEPT in singlefile mode where it is
    // the extraction directory <temp_dir>\<project> — the dir ExtractEmbeddedExe
    // unpacks the Go server into, so the default logging.dir of ${BIN_DIR} keeps
    // the exe and BOTH logs in one place.
    std::wstring binDir;
    // The resolved logging.dir, trailing separators stripped. Feeds InitLog here
    // and LaunchConfig::logDir for the Go log.
    std::wstring dir;
    // dir + L"\\" + <project> + L"_native.log".
    std::wstring path;
};

// Pure resolution — no filesystem, no Excel, no globals beyond the process
// environment that ExpandEnvVarsW reads. xllDir is GetXllDir()'s answer, passed
// in rather than queried so this is deterministically testable offline (see
// internal/assets/testdata/log_paths_native_test.cpp) and so the single
// module-path query stays visible at the one call site that makes it.
//
// Accepted forms of configuredDir, in the order they are tested:
//   - the legacy BARE tokens L"XLL_DIR" and L"TEMP_DIR" (whole-string match,
//     kept for projects generated before the ${} syntax existed);
//   - otherwise ${XLL_DIR} / ${BIN_DIR} are substituted and the result is run
//     through ExpandEnvVarsW, so ${TEMP} and any other environment variable
//     work too;
//   - empty falls back to binDir.
NativeLogPaths ResolveNativeLogPaths(const std::wstring& configuredDir,
                                     const std::string& projectName,
                                     const std::string& tempPattern,
                                     bool isSingleFile,
                                     const std::wstring& xllDir);

// ResolveNativeLogPaths(GetXllDir()) + InitLog + the outcome report, i.e. the
// single call xlAutoOpen makes. Returns the resolved paths so the caller can
// hand `dir` to LaunchConfig::logDir.
//
// A FAILED INIT IS NOT FATAL and must stay that way: logging is critical for
// debugging but not for core functionality, so the failure surfaces as a
// MessageBox (the log itself is the thing that just failed, so it cannot carry
// the message) and xlAutoOpen proceeds to load the add-in.
NativeLogPaths InitNativeLogging(const std::wstring& configuredDir,
                                 const std::string& level,
                                 const std::string& projectName,
                                 const std::string& tempPattern,
                                 bool isSingleFile);

void LogError(const std::string& msg);
void LogWarn(const std::string& msg);
void LogInfo(const std::string& msg);

// Teardown-path logger. UNLIKE LogInfo/LogWarn/LogError it is NOT suppressed by
// g_isUnloading, because the whole destructive teardown runs with that flag
// already latched and would otherwise be completely invisible in the log — the
// exact blindness that made the 2026-07-29 close-time use-after-unload crash so
// expensive to diagnose (the native log stopped at "Phase 1 done", which was
// misread as "Phase 2 never ran" when in fact Phase 2 had run and blocked
// inside a thread join). See AGENTS.md §20.2 / §23.6.
//
// SAFETY BOUND (do not widen): call this ONLY from the graceful teardown path,
// which runs on the STA and NOT under the loader lock. Do NOT call it from
// DllMain (any reason) or from a detached thread during a forced unload — that
// is what the g_isUnloading suppression in the other loggers exists to prevent.
void LogTeardown(const std::string& msg);

// WARN-level companion to LogTeardown. Same "not suppressed by g_isUnloading"
// contract and the same call-site restriction, but gated at WARN instead of INFO.
//
// WHY BOTH EXIST (review LOW #7): the teardown's narrative lines are INFO-worthy,
// but its FAILURE lines are not - a pin that did not take, a drain that timed out,
// a thread that had to be detached, a g_phost that had to be leaked. With a single
// INFO-gated logger every one of those vanished under `logging.level: warn`, which
// is exactly the configuration a user reporting a close-time problem is likely to
// be running. Route diagnosis-critical failures here; keep the step narration on
// LogTeardown.
void LogTeardownWarn(const std::string& msg);
#ifdef XLL_DEBUG_LOGGING
void LogDebug(const std::string& msg);
#else
inline void LogDebug(const std::string& msg) {}
#endif

} // namespace xll

// Safe logging macros: no-op during forced unload to avoid touching freed
// resources.
//
// These used to be defined in xll_main.cpp.tmpl, which meant only the generated
// TU could use them and every asset had to call xll::LogWarn directly — i.e. the
// asset code silently lost the unload guard the template code had. Defining them
// beside the loggers they wrap makes the guarded form the default everywhere.
//
// The guard reads xll::g_isUnloading, which is DEFINED in src/xll_lifecycle.cpp.
// It is declared here rather than by including xll_lifecycle.h, for two reasons:
//   1. xll_lifecycle.h already includes THIS header (for LogError, used by its
//      inline LogException), so including it back would be a cycle that only
//      happens to work because of where the two includes sit in each file.
//   2. xll_lifecycle.h transitively pulls in xll_launch.h and shm/Logger.h. A TU
//      that only wants to LOG should not need the shm include path — the offline
//      g++ harnesses in internal/assets (cache_cpp_test.go, gridarg_cpp_test.go)
//      compile single asset TUs with types/flatbuffers/phmap only, and stop
//      compiling the moment logging drags the lifecycle chain in.
// Declaring it here still means a call site cannot use the macros un-guarded by
// accident, which was the point of moving them out of the template.
#include <atomic>

namespace xll {
extern std::atomic<bool> g_isUnloading;
} // namespace xll

#ifndef SAFE_LOG_CALL
#define SAFE_LOG_CALL(level, msg) do { if (!xll::g_isUnloading) xll::level(msg); } while(0)
#define SAFE_LOG_INFO(msg) SAFE_LOG_CALL(LogInfo, msg)
#define SAFE_LOG_WARN(msg) SAFE_LOG_CALL(LogWarn, msg)
#define SAFE_LOG_ERROR(msg) SAFE_LOG_CALL(LogError, msg)
#ifdef XLL_DEBUG_LOGGING
#define SAFE_LOG_DEBUG(msg) SAFE_LOG_CALL(LogDebug, msg)
#else
#define SAFE_LOG_DEBUG(msg) do {} while(0)
#endif
#endif // SAFE_LOG_CALL
