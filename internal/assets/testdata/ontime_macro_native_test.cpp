// ontime_macro_native_test.cpp — EXECUTES xll::RegisterOnTimeMacro (declared
// inline in internal/assets/files/include/xll_lifecycle.h) against a stubbed
// xlfRegister and asserts the registration shape it produces.
//
// This replaces two 18-line xlfRegister blocks that used to sit
// in internal/templates/xll_main.cpp.tmpl's xlAutoOpen, where the only thing
// testing them was the rendered text. The properties below are the ones the
// text could not show: that the four constants ARE those constants at the point
// of the call, that a rejected registration is reported and survived rather than
// aborting the load, and that the register-ID XLOPER Excel filled in is released
// (the previous code depended on ScopedXLOPER12Result's destructor for that, and
// nothing checked it).
//
// Compiled and run by internal/assets/ontime_macro_cpp_test.go. Needs g++ and
// the types headers only — no shm, no FetchContent cache.

#include <cstdio>
#include <cstring>
#include <string>
#include <vector>

#include "xll_lifecycle.h"

// ---------------------------------------------------------------------------
// Recording stubs for everything the inline function bottoms out in.
// ---------------------------------------------------------------------------

struct RegisterCall {
    bool                      called = false;
    std::wstring              procedure;
    std::wstring              typeText;
    std::wstring              functionText;
    std::wstring              argumentText;
    int                       macroType = -1;
    std::wstring              category;
    std::wstring              shortcut;
    std::wstring              helpTopic;
    std::wstring              functionHelp;
    std::vector<std::wstring> argumentHelp;
    LPXLOPER12                regIdOut = nullptr;
    const XLOPER12*           dllOper  = nullptr;
};

static RegisterCall g_call;
static int          g_registerResult = xlretSuccess;
static int          g_freeCalls = 0;
static std::string  g_lastError;

namespace xll {
std::atomic<bool> g_isUnloading(false);

void LogError(const std::string& msg) { g_lastError = msg; }

int RegisterFunction(
    const XLOPER12& xDLL,
    const std::wstring& procedure,
    const std::wstring& typeText,
    const std::wstring& functionText,
    const std::wstring& argumentText,
    int macroType,
    const std::wstring& category,
    const std::wstring& shortcut,
    const std::wstring& helpTopic,
    const std::wstring& functionHelp,
    const std::vector<std::wstring>& argumentHelp,
    XLOPER12& xRegId
) {
    g_call.called       = true;
    g_call.procedure    = procedure;
    g_call.typeText     = typeText;
    g_call.functionText = functionText;
    g_call.argumentText = argumentText;
    g_call.macroType    = macroType;
    g_call.category     = category;
    g_call.shortcut     = shortcut;
    g_call.helpTopic    = helpTopic;
    g_call.functionHelp = functionHelp;
    g_call.argumentHelp = argumentHelp;
    g_call.regIdOut     = &xRegId;
    g_call.dllOper      = &xDLL;

    if (g_registerResult == xlretSuccess) {
        // What Excel does on success: it fills the result operand in. A STRING
        // register id is the case that must be released — a scalar would let a
        // missing free go unnoticed.
        xRegId.xltype  = xltypeStr;
        xRegId.val.str = nullptr;
    }
    return g_registerResult;
}
} // namespace xll

// ScopedXLOPER12Result::Free() releases a str/multi/ref result through
// Excel12(xlFree, ...). Counting those calls is how the test sees the release.
extern "C" int _cdecl Excel12(int xlfn, LPXLOPER12 /*operRes*/, int /*count*/, ...) {
    if (xlfn == xlFree) {
        g_freeCalls++;
    }
    return xlretSuccess;
}

// ---------------------------------------------------------------------------

static int g_failures = 0;

#define CHECK(cond, what)                                                       \
    do {                                                                        \
        if (!(cond)) {                                                          \
            std::printf("FAIL: %s  (%s:%d)\n", (what), __FILE__, __LINE__);      \
            g_failures++;                                                       \
        }                                                                       \
    } while (0)

static void reset(int registerResult) {
    g_call           = RegisterCall{};
    g_registerResult = registerResult;
    g_freeCalls      = 0;
    g_lastError.clear();
}

int main() {
    XLOPER12 xDLL{};
    xDLL.xltype = xltypeStr;

    // ---- 1. The registration shape ----------------------------------------
    //
    // macroType 2 is the load-bearing one: Excel resolves an ON.TIME target by
    // name against MACRO registrations, so a 1 here would leave both scheduled
    // macros silently unreachable (the deferred calc-end commands and date
    // formats would never be applied, and the ribbon retry would never tick).
    reset(xlretSuccess);
    bool ok = xll::RegisterOnTimeMacro(xDLL, L"__xllgen_RunDeferredCalcEnd", "deferred calc-end runner");

    CHECK(ok, "a successful xlfRegister must report success");
    CHECK(g_call.called, "RegisterOnTimeMacro did not call xlfRegister at all");
    CHECK(g_call.macroType == 2, "macroType must be 2 (registered macro), or xlcOnTime cannot target it");
    CHECK(g_call.typeText == L"I", "TypeText must be \"I\" to match the short __stdcall export");
    CHECK(g_call.procedure == L"__xllgen_RunDeferredCalcEnd", "Procedure must be the exported symbol name");
    CHECK(g_call.functionText == g_call.procedure,
          "FunctionText must equal Procedure: xlcOnTime targets the FunctionText");
    CHECK(g_call.argumentText.empty(), "the macro takes no arguments");
    CHECK(g_call.category.empty() && g_call.shortcut.empty() && g_call.helpTopic.empty() &&
              g_call.functionHelp.empty() && g_call.argumentHelp.empty(),
          "the macro must stay hidden: no category, shortcut or help");
    // The xlGetName DLL operand is xlfRegister's FIRST argument: it is how
    // Excel knows which loaded module owns the procedure. Substituting any
    // other XLOPER12 (a default-constructed one, a copy) makes every
    // registration target the wrong module or fail outright, and every check
    // above still passes because none of them looks at it. Identity, not
    // equality — the helper must FORWARD the caller's operand, not copy it.
    CHECK(g_call.dllOper == &xDLL, "the xlGetName DLL operand must be forwarded to xlfRegister");

    // ---- 2. The register id is released -----------------------------------
    //
    // xlfRegister's result is an Excel-allocated operand and the SDK contract
    // makes the caller free it. Registration happens once per macro per load,
    // so the leak is small — but it is the same class of defect the
    // ScopedXLOPER12Result comment in types was written for, and moving the
    // call into a helper is exactly where a hand-rolled XLOPER12 could lose it.
    CHECK(g_freeCalls == 1, "the xlfRegister result operand was not released via xlFree");

    // ---- 3. A rejected registration is reported, NOT fatal -----------------
    //
    // The add-in must still load: losing a deferred-work macro degrades a
    // feature, aborting xlAutoOpen loses the whole add-in.
    reset(xlretFailed);
    ok = xll::RegisterOnTimeMacro(xDLL, L"__xllgen_RibbonConnectRetry", "ribbon-connect OnTime retry");

    CHECK(!ok, "a rejected xlfRegister must report failure");
    CHECK(!g_lastError.empty(), "a rejected xlfRegister must be logged");
    CHECK(g_lastError.find("ribbon-connect OnTime retry") != std::string::npos,
          "the failure log must name WHICH macro failed");
    CHECK(g_lastError.find(std::to_string(xlretFailed)) != std::string::npos,
          "the failure log must carry the xlret code");

    // ---- 4. The name is the caller's, verbatim ----------------------------
    //
    // The two call sites pass xll::DeferredRunnerMacroName() /
    // xll::RibbonConnectRetryMacroName(), which are also what xlcOnTime
    // schedules and what the template exports. A helper that rewrote the name
    // (prefixing, casing) would break that three-way identity silently.
    reset(xlretSuccess);
    xll::RegisterOnTimeMacro(xDLL, L"__xllgen_RibbonConnectRetry", "ribbon-connect OnTime retry");
    CHECK(g_call.procedure == L"__xllgen_RibbonConnectRetry", "the name passed in must be used verbatim");
    CHECK(g_call.functionText == L"__xllgen_RibbonConnectRetry", "the name passed in must be used verbatim");

    // ---- 5. The unload guard does not swallow the failure report ----------
    //
    // SAFE_LOG_ERROR is a no-op while g_isUnloading is latched. That is correct
    // for teardown, but registration only ever runs at load, so the guard must
    // never be the reason a failure went unreported in practice.
    reset(xlretFailed);
    xll::g_isUnloading = true;
    xll::RegisterOnTimeMacro(xDLL, L"__xllgen_RunDeferredCalcEnd", "deferred calc-end runner");
    xll::g_isUnloading = false;
    CHECK(g_lastError.empty(), "SAFE_LOG_ERROR must stay suppressed while unloading");

    if (g_failures == 0) {
        std::printf("ALL OK\n");
        return 0;
    }
    std::printf("%d CHECK(s) FAILED\n", g_failures);
    return 1;
}
