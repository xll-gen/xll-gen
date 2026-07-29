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
