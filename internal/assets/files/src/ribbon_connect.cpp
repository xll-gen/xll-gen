// ribbon_connect.cpp — see include/com/ribbon_connect.h for the contract, in
// particular the injected ConnectContext (what the generated TU must publish and
// why GetExcelApplicationOrBounce stayed behind) and the deliberate cookie
// ownership split.
// Compiled only for ribbon builds, mirroring src/ribbon_addin.cpp and
// src/scratch_book.cpp: this TU is swept up by file(GLOB src/*.cpp) in every
// project, but the RibbonAddIn COM class it instantiates is itself declared only
// under XLL_RIBBON_ENABLED (and the COM libraries it needs are linked only when
// the ribbon is on).
//
// The gate opens BEFORE com/ribbon_connect.h: that header #errors when the macro
// is absent, precisely so a non-ribbon TU cannot declare symbols whose
// definitions this file does not compile. Including it above the gate would make
// every non-ribbon build fail here instead.
#ifdef XLL_RIBBON_ENABLED

#include "com/ribbon_connect.h"

#include "com/dispatch_helpers.h"
#include "com/ribbon_addin.h"        // RibbonAddIn, SetRibbonXml
#include "com/ribbon_image.h"        // SetRibbonImages
#include "rtd/factory.h"             // rtd::ClassFactory
#include "rtd/registry.h"            // rtd::RegisterServer / RegisterOfficeAddinKey
#include "types/utility.h"           // WideToUtf8 (Resiliency probe)
#include "types/xlcall.h"            // xlretSuccess
#include "xll_deferred_commands.h"   // xll::ScheduleOnTimeMacro / RibbonConnectRetryMacroName
#include "xll_lifecycle.h"           // xll::TeardownStarted / xll::g_isUnloading
#include "xll_log.h"                 // SAFE_LOG_*

#include <cstdio>
#include <cwctype>                   // std::towlower (case-folded Resiliency search)
#include <iterator>                  // std::size
#include <string>
#include <vector>

namespace xll {
namespace ribbon {

// Retry/connect state. See the header for the full rationale of each one — they
// are declared there (rather than kept private to this TU) because the exported
// __xllgen_RibbonConnectRetry macro and the xlAutoOpen arm site, both of which
// must live in the generated TU, read and charge them.
std::atomic<int>  g_ribbonConnectState{0}; // 0=pending, 1=connected, 2=gave up
std::atomic<bool> g_ribbonRegistered{false};

std::atomic<bool> g_ribbonRetryArmed{false};       // start-once latch for the chain
std::atomic<int>  g_ribbonRetryAttempts{0};        // productive attempts consumed
std::atomic<int>  g_ribbonRetryNoAppAttempts{0};   // "no workbook yet" attempts consumed

namespace {

// Published once by the generated xlAutoOpen. STA-only access, and written before
// anything below can run, so it needs no synchronization of its own.
ConnectContext g_ctx;

// The generated xlAutoOpen calls SetConnectContext before it registers the
// teardown hook and before the first connect attempt, so an unpublished context
// is UNREACHABLE in a generated project. It is checked anyway — loudly, once —
// because the alternative is calling a null function pointer or handing
// rtd::RegisterServer a null ProgID, i.e. a crash inside the COM registration
// path, and because this TU compiles into every ribbon build regardless of what a
// future template does. Not a behavior change: on every real project this
// predicate is true at every call.
bool ContextReady(const char* site) {
    // hModule is checked FIRST because it is the one field whose absence does
    // NOT fail loudly. It flows into rtd::RegisterServer, where a null HMODULE
    // makes GetModuleFileNameW resolve to the HOST process path — so the HKCU
    // InprocServer32 for our CLSID would be written pointing at EXCEL.EXE. That
    // is a persistent, user-scope registry entry that outlives the session and
    // sends a later CoCreateInstance at the wrong image. Every other field
    // either crashes or refuses immediately, which is why this one is easy to
    // leave out of a readiness check and the worst one to leave out.
    // (clsid has no sentinel value, so it cannot be checked here.)
    if (g_ctx.hModule && g_ctx.progId && g_ctx.comFriendlyName && g_ctx.addinFriendlyName &&
        g_ctx.addinDescription && g_ctx.ribbonXml && g_ctx.getImages &&
        g_ctx.acquireApp && g_ctx.acquireAppOrBounce && g_ctx.pClassObjectCookie) {
        return true;
    }
    static std::atomic<bool> s_warned{false};
    bool expected = false;
    if (s_warned.compare_exchange_strong(expected, true)) {
        SAFE_LOG_WARN(std::string("Ribbon: connect context was never published by xlAutoOpen (")
                      + site + "); ribbon UI disabled. This is a generator wiring bug.");
    }
    return false;
}

// ---------------------------------------------------------------------------
// Connect-failure DIAGNOSTICS (2026-08-03, backlog line 121). None of this runs
// on the DISCONNECT direction: SetRibbonConnected(false) is the teardown-time
// call made from GracefulComTeardownHook inside Phase 1, on the STA, in the
// ~80-100 ms window before Excel's FreeLibrary (AGENTS.md §20.2.1). Everything
// below is behind `if (!ok && connected)`.

const char* FaultName(RibbonConnectFault f) {
    switch (f) {
        case RibbonConnectFault::kNoComAddInsProperty:   return "Application.COMAddIns unreadable";
        case RibbonConnectFault::kProgIdNotInCollection: return "our ProgID is not in Excel's COMAddIns collection";
        case RibbonConnectFault::kConnectPutRejected:    return "Office REJECTED the Connect property put";
        default:                                        return "none";
    }
}

std::string HrToString(HRESULT hr) {
    char buf[16];
    snprintf(buf, sizeof(buf), "0x%08lX", static_cast<unsigned long>(hr));
    return buf;
}

// Per-class tallies so the give-up line can name the DOMINANT failure instead of
// whichever one happened last. Written only on the connect direction, read once.
std::atomic<int>  s_faultCount[4]{};
std::atomic<long> s_faultLastHr[4]{};

void RecordFault(RibbonConnectFault f, HRESULT hr) {
    const int i = static_cast<int>(f);
    if (i <= 0 || i > 3) return;
    s_faultCount[i].fetch_add(1, std::memory_order_relaxed);
    s_faultLastHr[i].store(static_cast<long>(hr), std::memory_order_relaxed);
}

RibbonConnectFault DominantFault(HRESULT* pHr) {
    int best = 0, bestCount = 0;
    for (int i = 1; i <= 3; ++i) {
        const int c = s_faultCount[i].load(std::memory_order_relaxed);
        if (c > bestCount) { bestCount = c; best = i; }
    }
    if (pHr) *pHr = static_cast<HRESULT>(best ? s_faultLastHr[best].load(std::memory_order_relaxed) : 0);
    return static_cast<RibbonConnectFault>(best);
}

// READ-ONLY probe of Office's crash-resiliency disable list. There is no
// documented per-ProgID key: the values under
// HKCU\Software\Microsoft\Office\<ver>\Excel\Resiliency\DisabledItems have opaque
// hashed NAMES and carry the item identity inside their REG_BINARY data as UTF-16
// text. So enumerate, count, and substring-search the blobs.
//
// The <ver> segment is ENUMERATED, not hard-coded "16.0": the Addins key we write
// is version-independent (Office\Excel\Addins), and a second, silently-wrong
// Office-version assumption in the same file is how a diagnostic starts lying.
std::string DescribeResiliencyDisabledItems() {
    std::wstring needle = g_ctx.progId ? g_ctx.progId : L"";
    for (auto& ch : needle) ch = static_cast<wchar_t>(std::towlower(ch));

    HKEY hOffice = nullptr;
    if (RegOpenKeyExW(HKEY_CURRENT_USER, L"Software\\Microsoft\\Office", 0,
                      KEY_ENUMERATE_SUB_KEYS, &hOffice) != ERROR_SUCCESS) {
        return "Resiliency: HKCU Office key unreadable";
    }

    std::string result;
    for (DWORD i = 0;; ++i) {
        wchar_t ver[256];
        DWORD verLen = static_cast<DWORD>(std::size(ver));
        if (RegEnumKeyExW(hOffice, i, ver, &verLen, nullptr, nullptr, nullptr, nullptr) != ERROR_SUCCESS) break;

        std::wstring sub = std::wstring(ver) + L"\\Excel\\Resiliency\\DisabledItems";
        HKEY hDisabled = nullptr;
        if (RegOpenKeyExW(hOffice, sub.c_str(), 0, KEY_QUERY_VALUE, &hDisabled) != ERROR_SUCCESS) continue;

        DWORD nValues = 0, maxNameLen = 0, maxDataLen = 0;
        RegQueryInfoKeyW(hDisabled, nullptr, nullptr, nullptr, nullptr, nullptr, nullptr,
                         &nValues, &maxNameLen, &maxDataLen, nullptr, nullptr);
        std::vector<wchar_t> name(maxNameLen + 1u, 0);
        std::vector<BYTE> data(maxDataLen + sizeof(wchar_t), 0);
        bool present = false;
        for (DWORD v = 0; v < nValues && !present; ++v) {
            DWORD nameLen = static_cast<DWORD>(name.size());
            DWORD dataLen = maxDataLen;
            if (RegEnumValueW(hDisabled, v, name.data(), &nameLen, nullptr, nullptr,
                              data.data(), &dataLen) != ERROR_SUCCESS) continue;
            std::wstring blob(reinterpret_cast<const wchar_t*>(data.data()), dataLen / sizeof(wchar_t));
            for (auto& ch : blob) ch = static_cast<wchar_t>(std::towlower(ch));
            if (!needle.empty() && blob.find(needle) != std::wstring::npos) present = true;
        }
        RegCloseKey(hDisabled);

        if (!result.empty()) result += ", ";
        result += "Resiliency[" + WideToUtf8(ver) + "]: " + std::to_string(nValues) +
                  " disabled item(s), this ProgID " + (present ? "PRESENT" : "absent");
    }
    RegCloseKey(hOffice);
    if (result.empty()) result = "Resiliency: no DisabledItems key under any Office version";
    return result;
}

// ONE-SHOT environment dump, on the STA, strictly read-only, and only for
// kConnectPutRejected (the one class that IS an Office refusal). Latched by a CAS
// because the connect retries up to 60 times and 60 registry sweeps in the log is
// not a diagnostic. Never writes: a probe that mutates the state it is measuring
// is worse than no probe (the same read-only discipline the UIA harnesses follow).
void DumpConnectEnvironmentOnce(IDispatch* pApp) {
    static std::atomic<bool> s_dumped{false};
    bool expected = false;
    if (!s_dumped.compare_exchange_strong(expected, true)) return;

    std::string out = "Ribbon: connect environment (one-shot) — ";

    // 1. The Addins key we ourselves wrote, and its LoadBehavior. We write 0 on a
    //    FRESH install (we connect programmatically, never at Excel startup) and
    //    PRESERVE whatever is there afterwards (2026-08-03) — Office records a
    //    user's tick in the COM Add-ins dialog as 3. So NEITHER value is a fault
    //    here: 0 is the untouched default and 3 is a user who ticked the box. What
    //    matters is whether the key is present at all. Reported, not judged.
    std::wstring addinsKey = L"Software\\Microsoft\\Office\\Excel\\Addins\\";
    addinsKey += (g_ctx.progId ? g_ctx.progId : L"");
    HKEY hAddin = nullptr;
    if (RegOpenKeyExW(HKEY_CURRENT_USER, addinsKey.c_str(), 0, KEY_QUERY_VALUE, &hAddin) == ERROR_SUCCESS) {
        DWORD loadBehavior = 0, type = 0, cb = sizeof(loadBehavior);
        if (RegQueryValueExW(hAddin, L"LoadBehavior", nullptr, &type,
                             reinterpret_cast<LPBYTE>(&loadBehavior), &cb) == ERROR_SUCCESS &&
            type == REG_DWORD) {
            out += "Addins key present, LoadBehavior=" + std::to_string(loadBehavior);
        } else {
            out += "Addins key present, LoadBehavior unreadable";
        }
        RegCloseKey(hAddin);
    } else {
        out += "Addins key ABSENT";
    }

    // 2. Has Excel disabled us after an earlier crash?
    out += "; " + DescribeResiliencyDisabledItems();

    // 3. How many COM add-ins does Excel list at all? Zero is the Trust Center
    //    "Disable all Application Add-ins" signature.
    VARIANT vAddins; VariantInit(&vAddins);
    if (pApp && SUCCEEDED(xll::com::GetProperty(pApp, L"COMAddIns", &vAddins)) &&
        vAddins.vt == VT_DISPATCH && vAddins.pdispVal) {
        VARIANT vCount; VariantInit(&vCount);
        if (SUCCEEDED(xll::com::GetProperty(vAddins.pdispVal, L"Count", &vCount))) {
            VARIANT vI4; VariantInit(&vI4);
            if (SUCCEEDED(VariantChangeType(&vI4, &vCount, 0, VT_I4))) {
                out += "; COMAddIns.Count=" + std::to_string(vI4.lVal);
            }
            VariantClear(&vI4);
        }
        VariantClear(&vCount);
    } else {
        out += "; COMAddIns.Count unavailable";
    }
    VariantClear(&vAddins);

    SAFE_LOG_WARN(out);
}

} // namespace

void SetConnectContext(const ConnectContext& ctx) { g_ctx = ctx; }

// See the header for WHY this is public. It is deliberately the SAME predicate
// the connect machinery uses (ContextReady), not a second copy of the field list:
// ConnectContext gains fields over time and a hand-written duplicate would drift
// into approving a partially wired context.
bool ConnectContextPublished() { return ContextReady("OnConnection"); }

bool SetRibbonConnected(bool connected, bool* pNoApp, bool allowBounce, RibbonConnectFault* pFault) {
    if (pNoApp) *pNoApp = false;
    if (pFault) *pFault = RibbonConnectFault::kNone;
    if (!ContextReady("SetRibbonConnected")) return false;
    IDispatch* pApp = allowBounce ? g_ctx.acquireAppOrBounce() : g_ctx.acquireApp();
    if (!pApp) {
        if (pNoApp) *pNoApp = true;
        return false;
    }

    bool ok = false;
    // Each step's HRESULT is CAPTURED, not consumed by SUCCEEDED() at the call
    // site: the three of them are the whole diagnostic (backlog line 121). The
    // fault starts at the FIRST step and advances as each one passes, so whichever
    // step we did not get past is the one named.
    RibbonConnectFault fault = RibbonConnectFault::kNoComAddInsProperty;
    HRESULT hrAddins = E_FAIL, hrItem = E_FAIL, hrPut = E_FAIL;
    {
        VARIANT vAddins; VariantInit(&vAddins);
        hrAddins = xll::com::GetProperty(pApp, L"COMAddIns", &vAddins);
        if (SUCCEEDED(hrAddins) && vAddins.vt == VT_DISPATCH && vAddins.pdispVal) {
            fault = RibbonConnectFault::kProgIdNotInCollection;
            VARIANT vProg; VariantInit(&vProg);
            vProg.vt = VT_BSTR;
            vProg.bstrVal = SysAllocString(g_ctx.progId);
            VARIANT vItem; VariantInit(&vItem);
            hrItem = xll::com::Invoke(vAddins.pdispVal, L"Item", DISPATCH_METHOD | DISPATCH_PROPERTYGET, { vProg }, &vItem);
            if (SUCCEEDED(hrItem) && vItem.vt == VT_DISPATCH && vItem.pdispVal) {
                fault = RibbonConnectFault::kConnectPutRejected;
                VARIANT vConn; VariantInit(&vConn);
                vConn.vt = VT_BOOL;
                vConn.boolVal = connected ? VARIANT_TRUE : VARIANT_FALSE;
                hrPut = xll::com::Invoke(vItem.pdispVal, L"Connect", DISPATCH_PROPERTYPUT, { vConn }, nullptr);
                ok = SUCCEEDED(hrPut);
            }
            VariantClear(&vItem);
            VariantClear(&vProg);  // frees the BSTR (caller owns args per dispatch_helpers contract)
        }
        VariantClear(&vAddins);
    }
    if (ok) fault = RibbonConnectFault::kNone;
    if (pFault) *pFault = fault;

    // DIAGNOSTIC, CONNECT DIRECTION ONLY. `connected == false` is the teardown
    // disconnect (GracefulComTeardownHook, Phase 1, on the STA in the window
    // before Excel's FreeLibrary) and must gain ZERO extra work — no log, no COM
    // property get, and above all no registry read. Hence the `&& connected`.
    //
    // SAFE_LOG_* and not LogTeardown*: g_isUnloading is still false at hook time
    // on both teardown paths, so these are not suppressed, and this is not a
    // teardown site.
    if (!ok && connected) {
        const HRESULT stepHr = (fault == RibbonConnectFault::kNoComAddInsProperty)   ? hrAddins
                             : (fault == RibbonConnectFault::kProgIdNotInCollection) ? hrItem
                                                                                     : hrPut;
        RecordFault(fault, stepHr);
        SAFE_LOG_WARN(std::string("Ribbon: COMAddIns connect step FAILED — ") + FaultName(fault) +
                      " (COMAddIns hr=" + HrToString(hrAddins) +
                      ", Item hr=" + HrToString(hrItem) +
                      ", Connect put hr=" + HrToString(hrPut) + ").");
        if (fault == RibbonConnectFault::kConnectPutRejected) {
            DumpConnectEnvironmentOnce(pApp);
        }
    }
    pApp->Release();
    return ok;
}

bool TryConnectRibbon(const char* phase, bool allowBounce, RibbonAttempt* pOutcome) {
    if (pOutcome) *pOutcome = RibbonAttempt::kNotAttempted;
    if (g_ribbonConnectState.load(std::memory_order_acquire) != 0) return true;
    // TeardownStarted(), not g_isUnloading alone: a confirmed teardown latches
    // g_isQuiescing in Phase 1 and deliberately keeps g_isUnloading FALSE across
    // Excel's whole RTD handshake, so the unload flag alone would let a connect
    // attempt run mid-teardown (AGENTS.md 20.2.1 rule 2). Unreachable today from the
    // retry runner, which gates on the same predicate first; kept consistent so the
    // next caller cannot reintroduce the hole.
    if (xll::TeardownStarted()) return false;
    // Unpublished context: nothing was attempted, so nothing may be charged (the
    // pOutcome default above already says kNotAttempted).
    //
    // But it MUST also be terminal, which the other kNotAttempted exits are not
    // required to be. Those are self-limiting — the unload bail resolves as the
    // teardown proceeds, and the STA re-entrancy bail can only happen while an
    // OUTER attempt is in flight on this same thread, which will itself produce
    // a real outcome. An unpublished context is neither: it is a permanent
    // condition, so without latching, __xllgen_RibbonConnectRetry would charge
    // nothing, see g_ribbonConnectState stay 0, and re-arm itself every
    // kRibbonRetryIntervalSec for the whole Excel session — the "an add-in that
    // polls the host forever is its own defect" outcome AGENTS.md §3 bounds.
    // Latching state=2 matches how the one-time registration failures (steps
    // 1-3 below) report an unrecoverable configuration fault: the caller's state
    // gate stops the chain on the very next dispatch. Do NOT instead give
    // kNotAttempted a budget — that reopens the accounting hole §23.6
    // FOLLOW-UP #2 closed.
    if (!ContextReady("TryConnectRibbon")) {
        g_ribbonConnectState.store(2, std::memory_order_release);
        return false;
    }

    // Re-entrancy guard. The temp-workbook bounce (xlcNew/xlcWorkbookInsert) and
    // the COMAddIns Connect both pump the STA message loop, so an Excel callback
    // (e.g. CalculationEnded -> TryConnectRibbon("calc end")) can re-enter this
    // function on the SAME thread while a connect is mid-flight — reaching a
    // second concurrent COMAddIns…Connect. Single STA thread, so this is
    // re-entrancy, not a data race; bail out (still "pending") if already inside.
    //
    // This exit stays kNotAttempted: an OnTime retry dispatched from inside the
    // outer connect's message pump must not pay for a connect it never made.
    static std::atomic<bool> s_inConnect{false};
    bool expected = false;
    if (!s_inConnect.compare_exchange_strong(expected, true)) return false;
    struct ConnectGuard {
        std::atomic<bool>& flag;
        ~ConnectGuard() { flag.store(false, std::memory_order_release); }
    } connectGuard{s_inConnect};

    // One-time, workbook-independent registration (steps 1-3). Done once;
    // guarded so retries skip straight to the connect.
    if (!g_ribbonRegistered.load(std::memory_order_acquire)) {
        // Unconditional, exactly as before: ContextReady above has already
        // established that both are wired (the generated ribbonXml literal and
        // the generated images accessor are mandatory for a ribbon build).
        xll::ribbon::SetRibbonXml(g_ctx.ribbonXml);
        xll::ribbon::SetRibbonImages(g_ctx.getImages());

        // 1. HKCU COM registration (CLSID/InprocServer32 + ProgID).
        if (FAILED(rtd::RegisterServer(g_ctx.hModule, g_ctx.clsid, g_ctx.progId, g_ctx.comFriendlyName))) {
            SAFE_LOG_WARN("Ribbon: HKCU COM registration failed (locked-down registry?); ribbon UI disabled.");
            g_ribbonConnectState.store(2, std::memory_order_release);
            return false;
        }
        // 2. Office Addins key so COMAddIns enumerates us.
        if (FAILED(rtd::RegisterOfficeAddinKey(g_ctx.progId, g_ctx.addinFriendlyName, g_ctx.addinDescription))) {
            SAFE_LOG_WARN("Ribbon: Office Addins key registration failed; ribbon UI disabled.");
            g_ribbonConnectState.store(2, std::memory_order_release);
            return false;
        }
        // 3. In-memory class object so CoCreateInstance resolves without a
        //    registry CLSID lookup (mirrors the RTD pattern).
        rtd::ClassFactory<RibbonAddIn>* pFactory = new rtd::ClassFactory<RibbonAddIn>();
        HRESULT hr = CoRegisterClassObject(g_ctx.clsid, pFactory, CLSCTX_INPROC_SERVER, REGCLS_MULTIPLEUSE, g_ctx.pClassObjectCookie);
        pFactory->Release();
        if (FAILED(hr)) {
            char hrBuf[16];
            snprintf(hrBuf, sizeof(hrBuf), "0x%08lX", static_cast<unsigned long>(hr));
            SAFE_LOG_WARN(std::string("Ribbon: CoRegisterClassObject failed: ") + hrBuf);
            g_ribbonConnectState.store(2, std::memory_order_release);
            return false;
        }
        g_ribbonRegistered.store(true, std::memory_order_release);
    }

    // 4. Connect through Application.COMAddIns (synchronous; Excel calls
    //    GetCustomUI during this call). Needs the EXCEL7 window. When
    //    allowBounce is true (xlAutoOpen path) the temp-workbook bounce inside
    //    GetExcelApplicationOrBounce materializes that window when no document
    //    is open, so this normally succeeds on the very first attempt.
    bool noApp = false;
    RibbonConnectFault fault = RibbonConnectFault::kNone;
    if (SetRibbonConnected(true, &noApp, allowBounce, &fault)) {
        if (pOutcome) *pOutcome = RibbonAttempt::kConnected;
        g_ribbonConnectState.store(1, std::memory_order_release);
        SAFE_LOG_INFO(std::string("Ribbon: COM add-in connected (") + phase + ").");
        return true;
    }

    // Still no Application object (only reachable if the bounce failed, or on a
    // calc-end / OnTime retry before a workbook exists): the user simply hasn't
    // opened a workbook. Do NOT consume the give-up budget — the defensive
    // calc-end fallback and the OnTime retry keep trying until a workbook
    // appears. Report the class outward so the OnTime runner can honor the same
    // rule with ITS budget (it used to charge these, exhausting ~60 s of retries
    // against an empty Excel).
    if (noApp) {
        if (pOutcome) *pOutcome = RibbonAttempt::kNoApp;
        return false;
    }

    // Real connect failure (Application reachable but Connect rejected): bound
    // it so a pathological host doesn't retry forever.
    //
    // The cap is NOT an unboundedness bug and must not be "fixed" as one: state=2
    // latches and every subsequent entry short-circuits on the state gate above.
    // What it actually costs is 60 STA COM round-trips — each an
    // XLMAIN->XLDESK->EXCEL7 walk plus a COMAddIns property get — spread over ~a
    // minute of retries. The defect was that all 60 were UNDIAGNOSABLE, which is
    // what the fault classification below fixes: name the DOMINANT class (not
    // whichever happened last) and carry its HRESULT.
    if (pOutcome) *pOutcome = RibbonAttempt::kRejected;
    (void)fault; // classified inside SetRibbonConnected; tallied per class there
    static std::atomic<int> s_attempts{0};
    if (s_attempts.fetch_add(1) + 1 >= 60) {
        g_ribbonConnectState.store(2, std::memory_order_release);
        HRESULT domHr = S_OK;
        const RibbonConnectFault dom = DominantFault(&domHr);
        SAFE_LOG_WARN(std::string("Ribbon: COMAddIns connect failed after 60 attempts; ribbon UI disabled. "
                      "Dominant fault: ") + FaultName(dom) + " (last hr=" + HrToString(domHr) + ").");
    }
    return false;
}

void ArmConnectRetry() {
    // If the ribbon did NOT connect at load (ribbon.bounce: off, or a bounce
    // that failed) the calc-end fallback is the only remaining trigger — and it
    // never fires for a workbook that is open but never recalculates (manual
    // calc mode / no-formula book), delaying the ribbon tab indefinitely. Arm a
    // bounded xlcOnTime retry from the CALLER's context: xlAutoOpen is a VALID
    // command context for xlc* (§23.6 HIGH #2), and every re-arm runs from the
    // retry macro's own dispatch (also a command context). No-op once
    // connected/gave-up (g_ribbonConnectState != 0). See RunConnectRetryTick /
    // AGENTS.md §3.
    //
    // The g_ribbonRetryArmed CAS makes this START-ONCE: a second xlAutoOpen in
    // the same process generation must not start a second chain against the same
    // counters. The schedule rc is INSPECTED, not discarded — a rejected arm means
    // the whole chain never starts, and silently degrading to "calc-end only"
    // without a log is how this stays invisible until a user reports a missing tab.
    bool retryExpected = false;
    if (!xll::g_isUnloading.load(std::memory_order_acquire) &&
        g_ribbonConnectState.load(std::memory_order_acquire) == 0 &&
        g_ribbonRetryArmed.compare_exchange_strong(retryExpected, true)) {
        int armRc = xll::ScheduleOnTimeMacro(xll::RibbonConnectRetryMacroName(), kRibbonRetryIntervalSec);
        if (armRc != xlretSuccess) {
            // Nothing is in flight — un-latch so a later xlAutoOpen may retry.
            g_ribbonRetryArmed.store(false, std::memory_order_release);
            if (armRc == xll::kOnTimeNotScheduledTeardown) {
                // Not a failure: the scheduler declined because teardown had
                // already started. Warning here would describe a broken retry
                // chain during an orderly shutdown. DEBUG keeps it traceable.
                SAFE_LOG_DEBUG("Ribbon: OnTime connect retry not armed — teardown already started.");
            } else {
                SAFE_LOG_WARN("Ribbon: OnTime connect retry could not be armed (xlcOnTime rc=" +
                              std::to_string(armRc) + "); falling back to the calc-end retry only, so the "
                              "ribbon tab may be delayed until the first recalculation.");
            }
        }
    }
}

void RunConnectRetryTick() {
    // Self-abort on ANY teardown (quiesce or unload): do NOT touch Excel and do
    // NOT re-arm. See the matching TryConnectRibbon gate above (20.2.1 rule 2).
    if (xll::TeardownStarted()) return;
    // Idempotent connect attempt (no bounce on retries: a workbook, if any,
    // already exists; the bounce is only for the xlAutoOpen no-workbook case).
    // The outcome CLASS decides which budget (if any) pays for this dispatch.
    RibbonAttempt retryOutcome = RibbonAttempt::kNotAttempted;
    TryConnectRibbon("ontime retry", /*allowBounce=*/false, &retryOutcome);
    // Stop once the connect resolves — connected (1) or gave-up (2).
    if (g_ribbonConnectState.load(std::memory_order_acquire) != 0) return;

    // Still pending: charge the attempt to the budget matching its CLASS, then
    // re-arm at the matching spacing. Charging noApp to the productive budget
    // is what made an empty Excel burn all 30 attempts in 60 s and miss the
    // workbook the user opened afterwards (MED fix, 2026-07-26).
    //
    // kNotAttempted charges NOTHING and simply re-arms at the productive
    // spacing: TryConnectRibbon bailed before touching COM (STA re-entrancy —
    // Excel dispatched this macro while an outer connect/bounce was pumping
    // the message loop). Billing that to the productive budget is the same
    // accounting defect as the noApp one, one branch further out: an attempt
    // that never happened must not shrink the window for the attempts that
    // will. It cannot spin unbounded — it requires an outer attempt in flight
    // on this same STA thread, and when that returns the next dispatch gets a
    // real, chargeable outcome (or the state gate above stops the chain).
    double nextDelaySec = kRibbonRetryIntervalSec;
    if (retryOutcome == RibbonAttempt::kNoApp) {
        int n = g_ribbonRetryNoAppAttempts.fetch_add(1) + 1;
        if (n >= kRibbonRetryNoAppMaxAttempts) {
            SAFE_LOG_WARN("Ribbon: OnTime connect retry gave up waiting for a workbook to be opened "
                          "(no Application object for the full bounded window); the calc-end fallback "
                          "remains the only trigger.");
            return;
        }
        if (n >= kRibbonRetryNoAppFastAttempts) nextDelaySec = kRibbonRetryNoAppIdleSec;
    } else if (retryOutcome == RibbonAttempt::kRejected) {
        int n = g_ribbonRetryAttempts.fetch_add(1) + 1;
        if (n >= kRibbonRetryMaxAttempts) {
            SAFE_LOG_WARN("Ribbon: OnTime connect retry exhausted its bounded budget; the calc-end fallback remains the only trigger.");
            return;
        }
    } else {
        SAFE_LOG_DEBUG("Ribbon: OnTime connect retry re-entered while a connect was in flight; "
                       "no budget charged (nothing was attempted).");
    }
    // Inspect the re-arm rc: a rejected xlcOnTime silently ENDS the chain, so
    // it must be visible in the log rather than looking like "still retrying".
    int reArmRc = xll::ScheduleOnTimeMacro(xll::RibbonConnectRetryMacroName(), nextDelaySec);
    if (reArmRc != xlretSuccess) {
        g_ribbonRetryArmed.store(false, std::memory_order_release);
        if (reArmRc == xll::kOnTimeNotScheduledTeardown) {
            // The tick's own TeardownStarted() gate passed at entry, but
            // TryConnectRibbon pumps the STA message loop, so Excel can deliver
            // OnBeginShutdown while this dispatch is mid-flight. Ending the chain
            // is then the CORRECT outcome, not a failure to report.
            SAFE_LOG_DEBUG("Ribbon: OnTime connect retry chain ends — teardown started during this tick.");
        } else {
            SAFE_LOG_WARN("Ribbon: OnTime connect retry could not re-arm (xlcOnTime rc=" +
                          std::to_string(reArmRc) + "); the retry chain ENDS here and the calc-end "
                          "fallback remains the only trigger.");
        }
    }
}

} // namespace ribbon
} // namespace xll

#endif // XLL_RIBBON_ENABLED
