// Offline unit test for the native log bootstrap's path resolution.
//
// WHAT THIS EXISTS FOR. Until 2026-08-02 the whole xlAutoOpen logging preamble
// — the ${BIN_DIR} derivation, the logging.dir resolution, the <proj>_native.log
// name, the InitLog call and its failure MessageBox — was inline in
// internal/templates/xll_main.cpp.tmpl. It carried no template variables in its
// LOGIC, so every generated project got a byte-identical re-emitted copy whose
// only test was a golden-string grep, and the four-way single-directory contract
// of AGENTS.md §18.12 (native log / Go log / launcher / standalone Go) had no
// executable check on the C++ side at all.
//
// The unit under test is xll::ResolveNativeLogPaths
// (internal/assets/files/{include/xll_log.h,src/xll_log.cpp}), the pure half of
// that preamble: xllDir is a parameter rather than a GetXllDir() call precisely
// so this can assert exact strings.
//
// TWO KINDS OF ASSERTION LIVE HERE:
//   1. Explicit expectations for each documented input shape.
//   2. A differential check against LegacyResolve() below — a VERBATIM copy of
//      the template lines that were deleted — over a matrix of inputs. That is
//      what discharges "the relocation changed no behavior"; it is the same
//      discipline as pkg/chunk's TestBuildFrame_ByteIdenticalToLegacy and
//      pkg/server's TestBuildRtdOnceGridResult_PresizedBytesIdentical.
//
// Build/run: driven by internal/assets/log_bootstrap_cpp_test.go
// (TestNativeLogPathsBehavior). Exit code 0 and "0 failures" on stdout mean pass.

#include "xll_log.h"
#include "xll_path.h"
#include "types/utility.h"

#include <atomic>
#include <cstdio>
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

std::string Narrow(const std::wstring& w) { return WideToUtf8(w); }

void CheckEq(const std::wstring& got, const std::wstring& want, const std::string& what) {
    ++g_checks;
    if (got != want) {
        ++g_failures;
        std::printf("FAIL: %s\n      got  %s\n      want %s\n", what.c_str(),
                    Narrow(got).c_str(), Narrow(want).c_str());
    }
}

// ---------------------------------------------------------------------------
// LegacyResolve — a VERBATIM copy of the deleted xll_main.cpp.tmpl lines.
//
// Reproduced from the pre-2026-08-02 template with the template actions
// substituted by the parameters they interpolated:
//
//     std::wstring binDir = GetXllDir();                       -> xllDir
//     {{if singlefile}}
//     binDir = ExpandEnvVarsW(L"{{.Build.TempDir}}");           -> tempPattern
//     while (trailing sep) binDir.pop_back();
//     binDir += L"\\{{.ProjectName}}";                          -> projectName
//     {{end}}
//     std::wstring logDir = L"{{.Logging.Dir}}";                -> configuredDir
//     ... XLL_DIR / TEMP_DIR / ${} branches ...
//     if (logDir.empty()) logDir = binDir;
//     while (trailing sep) logDir.pop_back();
//     std::wstring logPath = logDir + L"\\{{.ProjectName}}_native.log";
//
// DO NOT "clean this up" or route it through the new function: its whole value
// is being an independent second implementation.
// ---------------------------------------------------------------------------
struct LegacyPaths {
    std::wstring binDir;
    std::wstring dir;
    std::wstring path;
};

LegacyPaths LegacyResolve(const std::wstring& configuredDir, const std::string& projectName,
                          const std::string& tempPattern, bool isSingleFile,
                          const std::wstring& xllDir) {
    const std::wstring wProj = StringToWString(projectName);

    std::wstring binDir = xllDir;
    if (isSingleFile) {
        binDir = xll::ExpandEnvVarsW(StringToWString(tempPattern));
        while (!binDir.empty() && (binDir.back() == L'\\' || binDir.back() == L'/')) binDir.pop_back();
        binDir += L"\\" + wProj;
    }

    std::wstring logDir = configuredDir;
    if (logDir == L"XLL_DIR") {
        logDir = xllDir;
    } else if (logDir == L"TEMP_DIR") {
        logDir = xll::ExpandEnvVarsW(L"${TEMP}");
    } else {
        xll::ReplaceAll(logDir, L"${XLL_DIR}", xllDir);
        xll::ReplaceAll(logDir, L"${BIN_DIR}", binDir);
        logDir = xll::ExpandEnvVarsW(logDir);
    }
    if (logDir.empty()) logDir = binDir;
    while (!logDir.empty() && (logDir.back() == L'\\' || logDir.back() == L'/')) logDir.pop_back();

    LegacyPaths out;
    out.binDir = binDir;
    out.dir = logDir;
    out.path = logDir + L"\\" + wProj + L"_native.log";
    return out;
}

// ---------------------------------------------------------------------------
// 1. Explicit expectations
// ---------------------------------------------------------------------------

const wchar_t* kXllDir = L"C:\\addins\\bin";
const char* kProj = "Proj";

xll::NativeLogPaths R(const std::wstring& dir, bool singlefile = false,
                      const char* tempPattern = "") {
    return xll::ResolveNativeLogPaths(dir, kProj, tempPattern, singlefile, kXllDir);
}

// An unset logging.dir must land beside the XLL, not in the launch cwd.
void TestEmptyFallsBackToBinDir() {
    xll::NativeLogPaths p = R(L"");
    CheckEq(p.binDir, L"C:\\addins\\bin", "EmptyFallsBackToBinDir: binDir");
    CheckEq(p.dir, L"C:\\addins\\bin", "EmptyFallsBackToBinDir: dir");
    CheckEq(p.path, L"C:\\addins\\bin\\Proj_native.log", "EmptyFallsBackToBinDir: path");
}

// The two LEGACY BARE tokens are whole-string matches, kept for projects
// generated before the ${} syntax existed. They must not be treated as
// substrings: a real directory literally named e.g. "C:\XLL_DIR" is not the
// token.
void TestLegacyBareTokens() {
    CheckEq(R(L"XLL_DIR").dir, L"C:\\addins\\bin", "LegacyBareTokens: XLL_DIR");

    SetEnvironmentVariableW(L"TEMP", L"C:\\t\\tmp");
    CheckEq(R(L"TEMP_DIR").dir, L"C:\\t\\tmp", "LegacyBareTokens: TEMP_DIR");

    // Substring, not the token: must go down the placeholder path untouched.
    CheckEq(R(L"C:\\XLL_DIR\\logs").dir, L"C:\\XLL_DIR\\logs",
            "LegacyBareTokens: a path CONTAINING the token text is not the token");
}

void TestPlaceholders() {
    CheckEq(R(L"${XLL_DIR}\\logs").dir, L"C:\\addins\\bin\\logs", "Placeholders: ${XLL_DIR}");
    CheckEq(R(L"${BIN_DIR}\\logs").dir, L"C:\\addins\\bin\\logs", "Placeholders: ${BIN_DIR}");

    SetEnvironmentVariableW(L"XLLGEN_LOGTEST_DIR", L"D:\\envlogs");
    CheckEq(R(L"${XLLGEN_LOGTEST_DIR}").dir, L"D:\\envlogs",
            "Placeholders: a generic ${ENV_VAR} must expand too");
    CheckEq(R(L"${XLLGEN_LOGTEST_DIR}\\sub").dir, L"D:\\envlogs\\sub",
            "Placeholders: ${ENV_VAR} with a suffix");
}

// Trailing separators are stripped so the "dir + L\"\\\\\" + name" join below
// cannot produce a doubled separator — and so LaunchConfig::logDir, which the
// launcher joins the same way, agrees.
void TestTrailingSeparatorsStripped() {
    CheckEq(R(L"C:\\logs\\").dir, L"C:\\logs", "TrailingSeparators: backslash");
    CheckEq(R(L"C:\\logs/").dir, L"C:\\logs", "TrailingSeparators: forward slash");
    CheckEq(R(L"C:\\logs\\\\//").dir, L"C:\\logs", "TrailingSeparators: several, mixed");
    CheckEq(R(L"C:\\logs\\").path, L"C:\\logs\\Proj_native.log", "TrailingSeparators: path join");
}

// SINGLEFILE is the case §18.12 exists for: ${BIN_DIR} must be the EXTRACTION
// directory <temp_dir>\<project> (where ExtractEmbeddedExe unpacks the Go
// server), not the temp root and not the XLL's own directory — that is what
// keeps the exe and BOTH logs together.
void TestSinglefileBinDirIsTheExtractionDir() {
    xll::NativeLogPaths p = R(L"", /*singlefile=*/true, "C:\\t\\extract");
    CheckEq(p.binDir, L"C:\\t\\extract\\Proj", "Singlefile: binDir is <temp_dir>\\<project>");
    CheckEq(p.dir, L"C:\\t\\extract\\Proj", "Singlefile: empty logging.dir follows binDir");
    CheckEq(p.path, L"C:\\t\\extract\\Proj\\Proj_native.log", "Singlefile: path");

    // A trailing separator on build.temp_dir must not double up.
    CheckEq(R(L"", true, "C:\\t\\extract\\").binDir, L"C:\\t\\extract\\Proj",
            "Singlefile: trailing separator on temp_dir");

    // ${BIN_DIR} resolves to the extraction dir, not the XLL dir.
    CheckEq(R(L"${BIN_DIR}", true, "C:\\t\\extract").dir, L"C:\\t\\extract\\Proj",
            "Singlefile: ${BIN_DIR} is the extraction dir");

    // build.temp_dir itself may be a placeholder.
    SetEnvironmentVariableW(L"TEMP", L"C:\\t\\tmp");
    CheckEq(R(L"", true, "${TEMP}").binDir, L"C:\\t\\tmp\\Proj",
            "Singlefile: build.temp_dir expands");

    // ${XLL_DIR} still means the XLL's directory even in singlefile mode.
    CheckEq(R(L"${XLL_DIR}", true, "C:\\t\\extract").dir, L"C:\\addins\\bin",
            "Singlefile: ${XLL_DIR} is still the XLL directory");
}

// The file NAME is contract: README/TUTORIAL troubleshooting, the smoke test and
// the C++ launcher's <proj>_go.log twin all spell it <proj>_native.log.
void TestFileName() {
    xll::NativeLogPaths p = xll::ResolveNativeLogPaths(L"C:\\logs", "My-Proj", "", false, kXllDir);
    CheckEq(p.path, L"C:\\logs\\My-Proj_native.log", "FileName: <proj>_native.log");
}

// ---------------------------------------------------------------------------
// 2. Differential check against the deleted template code
// ---------------------------------------------------------------------------

struct MatrixCase {
    const wchar_t* configuredDir;
    const char* projectName;
    const char* tempPattern;
    bool isSingleFile;
    const wchar_t* xllDir;
};

void TestMatchesTheDeletedTemplateCode() {
    SetEnvironmentVariableW(L"TEMP", L"C:\\t\\tmp");
    SetEnvironmentVariableW(L"XLLGEN_LOGTEST_DIR", L"D:\\envlogs");

    const std::vector<MatrixCase> cases = {
        {L"", "Proj", "", false, L"C:\\addins\\bin"},
        {L"XLL_DIR", "Proj", "", false, L"C:\\addins\\bin"},
        {L"TEMP_DIR", "Proj", "", false, L"C:\\addins\\bin"},
        {L"BIN_DIR", "Proj", "", false, L"C:\\addins\\bin"},
        {L"C:\\logs", "Proj", "", false, L"C:\\addins\\bin"},
        {L"C:\\logs\\", "Proj", "", false, L"C:\\addins\\bin"},
        {L"C:\\logs//\\", "Proj", "", false, L"C:\\addins\\bin"},
        {L"${XLL_DIR}", "Proj", "", false, L"C:\\addins\\bin"},
        {L"${XLL_DIR}\\logs", "Proj", "", false, L"C:\\addins\\bin"},
        {L"${BIN_DIR}", "Proj", "", false, L"C:\\addins\\bin"},
        {L"${BIN_DIR}\\logs", "Proj", "", false, L"C:\\addins\\bin"},
        {L"${TEMP}", "Proj", "", false, L"C:\\addins\\bin"},
        {L"${XLLGEN_LOGTEST_DIR}\\a", "Proj", "", false, L"C:\\addins\\bin"},
        {L"${NO_SUCH_VAR_XLLGEN}", "Proj", "", false, L"C:\\addins\\bin"},
        {L"C:\\XLL_DIR\\logs", "Proj", "", false, L"C:\\addins\\bin"},
        // singlefile variants of the same shapes
        {L"", "Proj", "C:\\t\\extract", true, L"C:\\addins\\bin"},
        {L"", "Proj", "C:\\t\\extract\\", true, L"C:\\addins\\bin"},
        {L"", "Proj", "${TEMP}", true, L"C:\\addins\\bin"},
        {L"BIN_DIR", "Proj", "C:\\t\\extract", true, L"C:\\addins\\bin"},
        {L"TEMP_DIR", "Proj", "C:\\t\\extract", true, L"C:\\addins\\bin"},
        {L"${BIN_DIR}", "Proj", "C:\\t\\extract", true, L"C:\\addins\\bin"},
        {L"${BIN_DIR}\\logs", "Proj", "C:\\t\\extract", true, L"C:\\addins\\bin"},
        {L"${XLL_DIR}", "Proj", "C:\\t\\extract", true, L"C:\\addins\\bin"},
        {L"C:\\logs", "Proj", "C:\\t\\extract", true, L"C:\\addins\\bin"},
        // other project names / a UNC-ish xllDir
        {L"", "My-Proj", "", false, L"\\\\server\\share\\addins"},
        {L"${BIN_DIR}\\l", "My-Proj", "", false, L"\\\\server\\share\\addins"},
        {L"", "P", "C:\\t", true, L"D:\\x"},
    };

    for (size_t i = 0; i < cases.size(); ++i) {
        const MatrixCase& c = cases[i];
        const std::string tag = "MatchesTheDeletedTemplateCode[" + std::to_string(i) + " " +
                                Narrow(c.configuredDir) + (c.isSingleFile ? " singlefile" : "") + "]";
        LegacyPaths want = LegacyResolve(c.configuredDir, c.projectName, c.tempPattern,
                                         c.isSingleFile, c.xllDir);
        xll::NativeLogPaths got = xll::ResolveNativeLogPaths(c.configuredDir, c.projectName,
                                                             c.tempPattern, c.isSingleFile, c.xllDir);
        CheckEq(got.binDir, want.binDir, tag + " binDir");
        CheckEq(got.dir, want.dir, tag + " dir");
        CheckEq(got.path, want.path, tag + " path");
    }
}

// The resolved directory is the ONE directory both logs go in, so the native
// path must be exactly `dir` + the file name — nothing may re-derive it.
void TestPathIsDirPlusName() {
    const std::vector<std::wstring> dirs = {
        L"", L"XLL_DIR", L"TEMP_DIR", L"${BIN_DIR}", L"C:\\logs\\", L"${XLL_DIR}\\x",
    };
    for (size_t i = 0; i < dirs.size(); ++i) {
        xll::NativeLogPaths p = R(dirs[i]);
        CheckEq(p.path, p.dir + L"\\Proj_native.log",
                "PathIsDirPlusName[" + std::to_string(i) + "]");
        Check(p.dir.empty() || (p.dir.back() != L'\\' && p.dir.back() != L'/'),
              "PathIsDirPlusName[" + std::to_string(i) + "]: dir still ends in a separator");
    }
}

} // namespace

int main() {
    TestEmptyFallsBackToBinDir();
    TestLegacyBareTokens();
    TestPlaceholders();
    TestTrailingSeparatorsStripped();
    TestSinglefileBinDirIsTheExtractionDir();
    TestFileName();
    TestMatchesTheDeletedTemplateCode();
    TestPathIsDirPlusName();

    std::printf("log_paths_native_test: %d checks, %d failures\n", g_checks, g_failures);
    return g_failures == 0 ? 0 : 1;
}
