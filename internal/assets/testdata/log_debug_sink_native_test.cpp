// Offline unit test for the NO-LOG-FILE sink in src/xll_log.cpp.
//
// WHAT THIS EXISTS FOR. WriteLogUnconditional used to open with
// `if (g_logPath.empty()) return;`, and g_logPath is written in exactly one
// place: InitLog, reached only from InitNativeLogging, called only from
// xlAutoOpen. So every line logged before that call — or during a whole session
// after a FAILED InitLog — was discarded in silence.
//
// That is not an edge case. RibbonAddIn::OnConnection (src/ribbon_addin.cpp)
// refuses a COM activation in a session where xlAutoOpen NEVER RAN, and logs the
// refusal at WARN so a user who ticked the COM Add-ins row has something to look
// at. By construction there is no log file in that state, so the one line that
// made the refusal diagnosable was dropped EXACTLY when it mattered — while the
// code comment and the test rationale both asserted the opposite. Strengthening
// an assertion could not fix that; a sink that exists before xlAutoOpen could.
//
// The unit under test is therefore the sink SELECTION, asserted by EXECUTION:
// with no log file, a logged line must reach the process debug-output channel;
// with a log file, it must reach the FILE and the debug channel must stay quiet
// (a fallback that also fires once the file sink is up would double every line
// of every normal session).
//
// HOW THE DEBUG CHANNEL IS OBSERVED. OutputDebugStringW raises
// DBG_PRINTEXCEPTION_WIDE_C (0x4001000A) before it does anything else; a
// vectored exception handler sees that first, in-process, and resuming with
// EXCEPTION_CONTINUE_EXECUTION consumes it the way an attached debugger would.
// This needs no debug-output viewer running and no system-wide DBWIN_BUFFER, so
// two of these tests can run concurrently on one machine without interfering.
//
// Build/run: driven by internal/assets/log_debug_sink_cpp_test.go
// (TestNativeLogDebugSinkBehavior). Exit code 0 and "0 failures" mean pass.

#include "xll_log.h"
#include "types/utility.h"

#include <atomic>
#include <cstdio>
#include <fstream>
#include <sstream>
#include <string>
#include <vector>
#include <windows.h>

// Normally defined by src/xll_lifecycle.cpp; xll_log.h declares it for the
// SAFE_LOG_* macros and xll_log.cpp reads it in WriteLog.
namespace xll {
std::atomic<bool> g_isUnloading{false};
}
// Normally defined by src/xll_lifecycle.cpp; types/utility.h declares it.
HINSTANCE g_hModule = nullptr;

namespace {

int g_failures = 0;
int g_checks = 0;

void Check(bool ok, const std::string& what) {
    ++g_checks;
    if (!ok) {
        ++g_failures;
        std::printf("FAIL: %s\n", what.c_str());
    }
}

// --- debug-output capture ---------------------------------------------------

std::vector<std::string> g_captured; // UTF-8, in emission order

LONG CALLBACK CaptureDebugOutput(EXCEPTION_POINTERS* info) {
    const EXCEPTION_RECORD* r = info->ExceptionRecord;
    if (r->NumberParameters >= 2) {
        if (r->ExceptionCode == 0x4001000A /* DBG_PRINTEXCEPTION_WIDE_C */) {
            const wchar_t* s = reinterpret_cast<const wchar_t*>(r->ExceptionInformation[1]);
            g_captured.push_back(s ? WideToUtf8(std::wstring(s)) : std::string());
            return EXCEPTION_CONTINUE_EXECUTION;
        }
        if (r->ExceptionCode == 0x40010006 /* DBG_PRINTEXCEPTION_C */) {
            const char* s = reinterpret_cast<const char*>(r->ExceptionInformation[1]);
            g_captured.push_back(s ? std::string(s) : std::string());
            return EXCEPTION_CONTINUE_EXECUTION;
        }
    }
    return EXCEPTION_CONTINUE_SEARCH;
}

void ResetCapture() { g_captured.clear(); }

bool CapturedContains(const std::string& needle) {
    for (size_t i = 0; i < g_captured.size(); ++i) {
        if (g_captured[i].find(needle) != std::string::npos) return true;
    }
    return false;
}

std::string CapturedJoined() {
    std::string all;
    for (size_t i = 0; i < g_captured.size(); ++i) all += g_captured[i];
    return all;
}

// --- helpers ----------------------------------------------------------------

// A path whose PARENT element is an existing FILE (this executable), so InitLog
// cannot create the directory and cannot open the file. Deterministic on every
// machine, unlike a guessed unused drive letter.
std::wstring UncreatablePath() {
    wchar_t self[MAX_PATH];
    DWORD n = GetModuleFileNameW(NULL, self, MAX_PATH);
    if (n == 0 || n >= MAX_PATH) return L"";
    return std::wstring(self, n) + L"\\nested\\x.log";
}

std::wstring MakeTempLogPath() {
    wchar_t dir[MAX_PATH];
    DWORD n = GetTempPathW(MAX_PATH, dir);
    if (n == 0 || n >= MAX_PATH) return L"";
    wchar_t file[MAX_PATH];
    if (GetTempFileNameW(dir, L"xls", 0, file) == 0) return L"";
    return std::wstring(file);
}

std::string ReadWholeFile(const std::wstring& path) {
    std::ifstream in(path.c_str(), std::ios::binary);
    if (!in.is_open()) return "";
    std::ostringstream ss;
    ss << in.rdbuf();
    return ss.str();
}

// ---------------------------------------------------------------------------
// 1. No log file has ever been opened — the state RibbonAddIn::OnConnection's
//    refusal guard runs in. Every level that survives its own g_logLevel gate
//    must reach the debug channel.
// ---------------------------------------------------------------------------
void TestFallbackBeforeAnyInit() {
    // The refusal line's own level. This is THE case the sink exists for.
    ResetCapture();
    xll::LogWarn("Ribbon: OnConnection REFUSED - sentinel-warn");
    Check(CapturedContains("Ribbon: OnConnection REFUSED - sentinel-warn"),
          "FallbackBeforeAnyInit: a WARN logged with no log file must reach the debug-output "
          "channel (this is the OnConnection refusal's only sink)");
    // The LEVEL has to survive into the fallback: a sink that emits the bare
    // message loses the one field that says how bad it is.
    Check(CapturedContains("[WARN]"),
          "FallbackBeforeAnyInit: the fallback line must carry the level tag, got: " + CapturedJoined());
    // A marker that says which process wrote it — the debug channel is shared by
    // every module in the host.
    Check(CapturedContains("[xll]"),
          "FallbackBeforeAnyInit: the fallback line must carry an [xll] marker, got: " + CapturedJoined());

    ResetCapture();
    xll::LogError("sentinel-error");
    Check(CapturedContains("sentinel-error"), "FallbackBeforeAnyInit: ERROR reaches the debug channel");

    ResetCapture();
    xll::LogInfo("sentinel-info");
    Check(CapturedContains("sentinel-info"),
          "FallbackBeforeAnyInit: INFO reaches the debug channel (the default level is INFO)");
}

// The teardown loggers go through the same choke point and are NOT gated on
// g_isUnloading, so they inherit the fallback too. Asserted rather than assumed:
// they are the other family of call sites that can run outside a live log file.
void TestTeardownLoggersFallBackToo() {
    ResetCapture();
    xll::LogTeardownWarn("sentinel-teardown-warn");
    Check(CapturedContains("sentinel-teardown-warn"),
          "TeardownLoggersFallBackToo: LogTeardownWarn must reach the debug channel with no log file");

    ResetCapture();
    xll::LogTeardown("sentinel-teardown");
    Check(CapturedContains("sentinel-teardown"),
          "TeardownLoggersFallBackToo: LogTeardown must reach the debug channel with no log file");
}

// The g_isUnloading suppression is UPSTREAM of the sink choice and must stay
// that way: the ordinary loggers are silent during a forced unload whether or
// not a log file exists. The fallback must not become a way around it.
void TestUnloadingStillSuppressesTheOrdinaryLoggers() {
    xll::g_isUnloading.store(true);
    ResetCapture();
    xll::LogWarn("sentinel-while-unloading");
    Check(!CapturedContains("sentinel-while-unloading"),
          "UnloadingStillSuppresses: the g_isUnloading guard must still silence LogWarn; the "
          "no-log-file fallback is not an exemption from it, got: " + CapturedJoined());
    xll::g_isUnloading.store(false);
}

// ---------------------------------------------------------------------------
// 2. A FAILED InitLog leaves g_logPath empty for the whole session. Before the
//    fallback existed that was a session-long blackout after a single message
//    box. The level parsed by that same failed call must still be honoured.
// ---------------------------------------------------------------------------
void TestFailedInitKeepsTheFallbackLive() {
    const std::wstring bogus = UncreatablePath();
    Check(!bogus.empty(), "FailedInitKeepsTheFallbackLive: could not build an uncreatable path");
    if (bogus.empty()) return;

    std::string err;
    const bool ok = xll::InitLog(bogus, "warn", "", "Proj", false, err);
    Check(!ok, "FailedInitKeepsTheFallbackLive: InitLog must FAIL for a path under an existing file");
    Check(!err.empty(), "FailedInitKeepsTheFallbackLive: a failed InitLog must report why");

    ResetCapture();
    xll::LogWarn("sentinel-after-failed-init");
    Check(CapturedContains("sentinel-after-failed-init"),
          "FailedInitKeepsTheFallbackLive: after a failed InitLog every line still has to go "
          "somewhere; got: " + CapturedJoined());

    // …but the level gate that failed call parsed is still in force.
    ResetCapture();
    xll::LogInfo("sentinel-info-below-warn");
    Check(!CapturedContains("sentinel-info-below-warn"),
          "FailedInitKeepsTheFallbackLive: INFO must stay below `logging.level: warn` on the "
          "fallback path too, got: " + CapturedJoined());

    // `logging.level: none` must silence the fallback completely — the fallback
    // is a sink swap, not a policy override.
    std::string err2;
    xll::InitLog(bogus, "none", "", "Proj", false, err2);
    ResetCapture();
    xll::LogError("sentinel-must-be-silenced");
    Check(!CapturedContains("sentinel-must-be-silenced"),
          "FailedInitKeepsTheFallbackLive: `logging.level: none` must silence the debug-output "
          "fallback as well, got: " + CapturedJoined());
}

// ---------------------------------------------------------------------------
// 3. Once the file sink is up the fallback must be INERT. A fallback that also
//    fired would duplicate every line of every normal session onto a channel
//    nobody asked for.
// ---------------------------------------------------------------------------
void TestFileSinkSilencesTheFallback() {
    const std::wstring path = MakeTempLogPath();
    Check(!path.empty(), "FileSinkSilencesTheFallback: could not build a temp log path");
    if (path.empty()) return;

    std::string err;
    const bool ok = xll::InitLog(path, "info", "", "Proj", false, err);
    Check(ok, "FileSinkSilencesTheFallback: InitLog must succeed for a writable temp path: " + err);
    if (!ok) return;

    ResetCapture();
    xll::LogWarn("sentinel-goes-to-the-file");

    Check(!CapturedContains("sentinel-goes-to-the-file"),
          "FileSinkSilencesTheFallback: with a live log file NOTHING may reach the debug-output "
          "channel, got: " + CapturedJoined());

    const std::string contents = ReadWholeFile(path);
    Check(contents.find("sentinel-goes-to-the-file") != std::string::npos,
          "FileSinkSilencesTheFallback: the line must land in the log FILE; file held: " + contents);
    Check(contents.find("[WARN]") != std::string::npos,
          "FileSinkSilencesTheFallback: the file line must carry its level");

    DeleteFileW(path.c_str());
}

} // namespace

int main() {
    PVOID veh = AddVectoredExceptionHandler(/*first=*/1, CaptureDebugOutput);
    if (!veh) {
        std::printf("FAIL: AddVectoredExceptionHandler returned NULL; cannot observe the debug channel\n");
        std::printf("log_debug_sink_native_test: 0 checks, 1 failures\n");
        return 1;
    }

    // Self-check FIRST: if the capture mechanism itself is broken, every
    // assertion below would report a missing line and blame the code under test.
    ResetCapture();
    OutputDebugStringW(L"xllgen-capture-self-check");
    if (!CapturedContains("xllgen-capture-self-check")) {
        std::printf("FAIL: the vectored-handler capture does not observe OutputDebugStringW on this "
                    "machine; the assertions below would be meaningless\n");
        std::printf("log_debug_sink_native_test: 1 checks, 1 failures\n");
        RemoveVectoredExceptionHandler(veh);
        return 1;
    }
    ++g_checks;

    // ORDER IS LOAD-BEARING: g_logPath and g_logLevel are file statics with no
    // reset, and InitLog only ever sets g_logPath. Everything that needs "no log
    // file" must run before the one successful InitLog, which is last.
    TestFallbackBeforeAnyInit();
    TestTeardownLoggersFallBackToo();
    TestUnloadingStillSuppressesTheOrdinaryLoggers();
    TestFailedInitKeepsTheFallbackLive();
    TestFileSinkSilencesTheFallback();

    RemoveVectoredExceptionHandler(veh);
    std::printf("log_debug_sink_native_test: %d checks, %d failures\n", g_checks, g_failures);
    return g_failures == 0 ? 0 : 1;
}
