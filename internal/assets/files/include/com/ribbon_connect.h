#pragma once
// ribbon_connect.h — the ribbon COM add-in bootstrap ("connect") machinery.
//
// This is the code that makes the native ribbon tab appear: HKCU COM
// registration, the Office Addins key, the in-memory class object, and the
// Application.COMAddIns.Item(progId).Connect = true that actually loads the
// add-in. It also owns the bounded retry-chain state that the generated
// __xllgen_RibbonConnectRetry OnTime macro drives, and the DISCONNECT half that
// the generated graceful-teardown hook calls.
//
// WHY IT LIVES HERE. All of it was inline in xll_main.cpp.tmpl and contained NO
// template variables, so every project got a byte-identical copy re-emitted into
// its generated tree — where nothing but a golden-string grep could test it.
// Compiled once as an ordinary asset it is greppable, reviewable C++, and the
// generator tests can assert the WIRING instead of restating the body.
//
// This is a PURE RELOCATION (2026-08-02). The state gate, the two-budget
// accounting, the re-entrancy guard, the 60-attempt cap and every log string are
// exactly what shipped. The comments moved WITH the code because they record
// measured findings, not intent — AGENTS.md §18.11 (deferred-connect + give-up
// budget semantics), §20.3 (the temp-workbook bounce and its STA re-entrancy
// hazard) and §23.6 "§3" + its two FOLLOW-UPs (the OnTime retry, the noApp
// budget split, the never-attempted accounting hole). Do not summarize them away.
//
// WHAT DELIBERATELY DID NOT MOVE: GetExcelApplicationOrBounce(). Its body is
// {{if .Ribbon.BounceMode}}-branched (full / keep-open / off) — real generated
// code — so it stays in the template and arrives here as a function pointer.
//
// GATING: src/ribbon_connect.cpp compiles its body only under
// XLL_RIBBON_ENABLED (CMake defines it project-wide for ribbon builds), because
// file(GLOB src/*.cpp) sweeps this TU into EVERY generated project while the COM
// class it instantiates (RibbonAddIn) is itself declared only under that macro.
// Same pattern as src/ribbon_addin.cpp and src/scratch_book.cpp.
//
// THREADING: STA only. xlAutoOpen, the CalculationEnded callback, the OnTime
// retry macro's dispatch and the COM teardown hook all run on Excel's main STA
// thread. The atomics below are re-entrancy / idempotence / start-once guards for
// that ONE thread (the COMAddIns Connect and the temp-workbook bounce both PUMP
// the message loop, so Excel can dispatch a callback mid-connect) — they are not
// cross-thread synchronization.

// GATING. The definitions of everything declared below live in
// src/ribbon_connect.cpp, which is #ifdef XLL_RIBBON_ENABLED. Declaring them in
// a non-ribbon TU therefore compiles cleanly and then fails at LINK with
// unresolved xll::ribbon::SetConnectContext / g_ribbonConnectState — a
// diagnostic one step removed from the cause. Fail at the include instead, the
// way com/ribbon_addin.h gates its class.
#ifndef XLL_RIBBON_ENABLED
#error "com/ribbon_connect.h requires XLL_RIBBON_ENABLED (its definitions are compiled only for ribbon builds)"
#endif

#include <windows.h>
#include <oaidl.h>
#include <atomic>
#include <vector>

#include "com/ribbon_image.h" // RibbonImage — the images accessor's return type

namespace xll {
namespace ribbon {

// Everything the connect machinery needs that ONLY the generated translation
// unit can know. xlAutoOpen fills it ONCE, before the graceful-teardown hook is
// registered and before the first connect attempt; this TU then never touches a
// generated symbol directly.
//
// The generated side of each field (internal/templates/xll_main.cpp.tmpl):
//   hModule            g_hModule (declared in xll_lifecycle.h)
//   progId             g_szRibbonProgID      = L"<ribbon.prog_id>"
//   clsid              GetRibbonClsid()      = <ribbon.clsid>
//   comFriendlyName    L"<ProjectName> Ribbon"           (rtd::RegisterServer)
//   addinFriendlyName  L"<ProjectName>"                  (rtd::RegisterOfficeAddinKey)
//   addinDescription   L"<ProjectName> ribbon helper"    (rtd::RegisterOfficeAddinKey)
//   ribbonXml          kXllRibbonXml         (generated ribbon_xml.h)
//   getImages          &GetXllRibbonImages   (generated ribbon_images.h)
//   acquireApp         &GetExcelApplication
//   acquireAppOrBounce &GetExcelApplicationOrBounce
//   pClassObjectCookie &g_ribbonCookie
//
// hModule is injected even though xll_lifecycle.h already publishes g_hModule:
// one struct is then the WHOLE contract between the generated TU and this one,
// which is what makes "did the template wire me up?" a single question.
//
// getImages is a FUNCTION POINTER, not a materialized vector. Today
// GetXllRibbonImages() runs lazily inside the one-time registration block, at
// most once per process; storing the vector here would build and copy every
// embedded icon at xlAutoOpen even on a load that never registers. Deferring the
// call keeps the allocation profile identical — this is a relocation, including
// of the cost.
//
// COOKIE OWNERSHIP — DELIBERATE CHOICE (2026-08-02). The CoRegisterClassObject
// cookie is WRITTEN here (in the one-time registration) and READ by the
// generated GracefulComTeardownHook, which revokes it. It stays a global in the
// TEMPLATE and this TU writes it through pClassObjectCookie, rather than living
// here behind a getter/clearer, for three reasons:
//   1. GracefulComTeardownHook is left BYTE-IDENTICAL. Its statement ORDER —
//      explicit COMAddIns disconnect (or the documented skip), THEN
//      CoRevokeClassObject — is the fix for a 100%-reproducible mso.dll
//      NULL-vtable crash and is pinned by both
//      internal/generator/gen_office_disconnect_guard_test.go and
//      internal/assets/office_disconnect_guard_cpp_test.go. Two crash releases
//      (v0.8.41, v0.8.42) came out of this path; a relocation must not perturb
//      it at all — not even to route two statements through accessors.
//   2. The cookie is a sibling of GetRibbonClsid() and g_szRibbonProgID, which
//      MUST stay in the template because they carry template variables. Keeping
//      the whole COM-identity trio in one TU beats splitting one concept in half.
//   3. Single writer, single reader, both on the STA, one assignment each. There
//      is no invariant here for an accessor to protect.
struct ConnectContext {
    HMODULE        hModule            = nullptr;
    const wchar_t* progId             = nullptr;
    CLSID          clsid{};
    const wchar_t* comFriendlyName    = nullptr;
    const wchar_t* addinFriendlyName  = nullptr;
    const wchar_t* addinDescription   = nullptr;
    const wchar_t* ribbonXml          = nullptr;
    std::vector<RibbonImage> (*getImages)()         = nullptr;
    IDispatch*               (*acquireApp)()        = nullptr;
    IDispatch*               (*acquireAppOrBounce)() = nullptr;
    DWORD*         pClassObjectCookie = nullptr;
};

// Publishes the context. Call ONCE from xlAutoOpen, on the STA, BEFORE the
// graceful-teardown hook is registered (its disconnect needs acquireApp) and
// before the first TryConnectRibbon. Copied by value; every pointer in it
// designates a static or a string literal in the generated TU, all of which
// outlive the process.
void SetConnectContext(const ConnectContext& ctx);

// Sets Application.COMAddIns.Item(progId).Connect = <connected>. Used both
// to connect at xlAutoOpen and — critically — to DISCONNECT at teardown:
// revoking the class object does not release the live add-in instance Excel
// holds; without an explicit Connect=false Excel can keep a vtable pointer
// into this DLL past FreeLibrary and crash on quit (Excel-DNA does the same
// explicit disconnect for its helper add-in).
//
// pNoApp (optional, out): set to true when the failure is solely because the
// in-process Application object is not reachable yet (no workbook window -> no
// EXCEL7 child). Callers use this to avoid burning the give-up budget on the
// no-workbook-yet case (the user may open a workbook minutes later).
//
// allowBounce: when true, acquire the Application via the context's
// acquireAppOrBounce (synchronous temp-workbook bounce when no workbook is
// open). Only the xlAutoOpen first-attempt path passes true; calc-end retries
// must not bounce (a workbook already exists there) and pass false.
//
// [This paragraph came from the forward declaration the template used to carry
// above xlAutoClose, which is why the default arguments live here now:]
// allowBounce (default false): when true AND the in-process Application object
// is not reachable (no workbook -> no EXCEL7 child window), do the synchronous
// temp-workbook bounce to materialize it. Only the xlAutoOpen first-attempt
// path passes true; the calc-end retry path (a workbook already exists there)
// must never bounce. See GetExcelApplicationOrBounce in xll_main.cpp.
bool SetRibbonConnected(bool connected, bool* pNoApp = nullptr, bool allowBounce = false);

// Ribbon COM add-in bootstrap, made idempotent + retryable. The COMAddIns
// connect (step 4) needs the in-process Application object, which is reachable
// only through the XLDESK -> EXCEL7 child window. When the XLL loads with NO
// workbook open (add-in auto-loaded at Excel startup), that child window does
// not exist yet, so GetExcelApplication() returns nullptr.
//
// Primary mechanism: the xlAutoOpen first attempt passes allowBounce=true, so
// SetRibbonConnected -> GetExcelApplicationOrBounce synchronously materializes
// a workbook (Excel-DNA's temp-workbook bounce) and the connect succeeds even
// with no document open. This replaces the former STA WM_TIMER retry loop,
// which was an accepted crash residual on forced unload (AGENTS.md §20.2).
//
// Defensive fallback: the calc-end callback retries TryConnectRibbon WITHOUT
// bouncing (allowBounce=false). It is an Excel-registered event callback (no
// idle-timer unmap hazard) and only matters in the rare case the bounce itself
// fails (e.g. C API unavailable). It is harmless and idempotent.
//
// State is a single atomic: 0=pending, 1=connected, 2=gave up after bounded
// attempts. Registry/class-object steps (1-3) are themselves idempotent, but
// we only redo the cheap, workbook-dependent connect (step 4) on retry once
// the one-time registration has succeeded.
extern std::atomic<int>  g_ribbonConnectState; // 0=pending, 1=connected, 2=gave up
extern std::atomic<bool> g_ribbonRegistered;

// Bounded xlcOnTime connect-retry budget (AGENTS.md §3 / §20 / §23). Fills the
// gap the calc-end fallback misses: when the ribbon does NOT connect at load
// (ribbon.bounce: off, or a bounce that failed) the ONLY remaining trigger is
// TryConnectRibbon("calc end") — which never runs if a workbook is open but
// never recalculates (manual calc mode / no-formula book), so the ribbon tab
// would be delayed indefinitely. The retry macro (__xllgen_RibbonConnectRetry)
// re-arms itself from its OWN Excel-dispatched macro context — a VALID command
// context for xlc* (§23.6 HIGH #2) — and TERMINATES BY STATE GATE / SELF-ABORT
// (connect success, give-up, budget exhaustion, or g_isUnloading), never by a
// C-API cancel, so it adds NO new teardown-cancellation surface (§20/§23).
// Bounded so an idle no-workbook Excel cannot retry forever.
//
// TWO BUDGETS, because the retry has TWO distinct failure classes and they have
// completely different expected durations (MED fix, 2026-07-26):
//
//   * PRODUCTIVE failure — the Application object IS reachable (an EXCEL7 window
//     exists) but COMAddIns Connect was rejected. That is a real fault; polling
//     it for minutes helps nobody, so it keeps the original tight budget.
//   * "noApp" — no Application object yet, i.e. Excel is sitting empty and the
//     user simply has not opened a workbook. That is NOT a failure at all;
//     TryConnectRibbon has always deliberately declined to charge it against its
//     own give-up budget (see the `if (noApp)` early return in the .cpp). The
//     retry runner used to charge it anyway, so an empty Excel burned all 30
//     attempts in 60 s — and the very scenario this retry exists for (empty
//     Excel + bounce: off, user opens a manual-calc / no-formula workbook ~90 s
//     later) found the budget already spent AND no calc-end to fall back on. The
//     noApp class therefore gets its own, far longer, time-shaped budget.
//
// The noApp budget stays responsive for the first ~30 s (a workbook opened right
// after startup gets the tab within ~2 s) and then relaxes to a 10 s poll so a
// long idle wait costs ~6 dispatches/minute instead of 30. Total noApp window:
// 15*2 s + 60*10 s = 630 s (~10.5 min). It is deliberately FINITE — an add-in
// that polls Excel forever is its own defect (§23) — so an Excel left empty for
// longer than that gives up and falls back to calc-end only. See AGENTS.md §3
// for the documented residual hole.
inline constexpr int    kRibbonRetryMaxAttempts       = 30;   // productive attempts (~60 s @ 2.0 s)
inline constexpr double kRibbonRetryIntervalSec       = 2.0;  // spacing while progress is possible
inline constexpr int    kRibbonRetryNoAppFastAttempts = 15;   // first ~30 s of "no workbook yet" stay at 2.0 s
inline constexpr double kRibbonRetryNoAppIdleSec      = 10.0; // …then relax the poll
inline constexpr int    kRibbonRetryNoAppMaxAttempts  = 75;   // hard stop: 15*2 s + 60*10 s = 630 s

// Retry-chain state at FILE SCOPE (MED fix, 2026-07-26). These were function-local
// statics inside __xllgen_RibbonConnectRetry's SEH __try block. Two problems with
// that: (1) a second xlAutoOpen in the same process generation (probe-unload-reuse,
// or add-in disable→enable without a DLL unload) while still unconnected armed a
// SECOND self-re-arming chain sharing the same counter — halving the effective
// budget and doubling the dispatch rate; (2) a function-local static's guard
// variable is initialized inside an SEH __try scope, which is exactly the kind of
// hidden non-trivial construction the XLL_SAFE_BLOCK discipline avoids. File-scope
// atomics with constant initializers have neither problem.
//
// g_ribbonRetryArmed is a START-ONCE latch, not a liveness flag: the CAS at the
// xlAutoOpen arm site means at most ONE chain ever exists. It is cleared only when
// the xlcOnTime schedule itself was REJECTED (nothing is in flight, so a later
// xlAutoOpen may legitimately try again); the terminal states (connected, gave up,
// budget exhausted) leave it latched so nothing can restart an already-decided chain.
//
// They are declared here rather than kept private to this TU because the OnTime
// retry runner that consumes them — __xllgen_RibbonConnectRetry — is an EXPORTED
// macro symbol and therefore has to stay in the generated TU (Excel resolves the
// registered procedure by name against it). The arm site in xlAutoOpen reads
// g_ribbonConnectState and CASes g_ribbonRetryArmed for the same reason.
extern std::atomic<bool> g_ribbonRetryArmed;      // start-once latch for the chain
extern std::atomic<int>  g_ribbonRetryAttempts;   // productive attempts consumed
extern std::atomic<int>  g_ribbonRetryNoAppAttempts; // "no workbook yet" attempts consumed

// Outcome CLASS of one TryConnectRibbon call. The bool return only says
// "connected or not"; a caller that owns a retry budget needs to know WHICH KIND
// of not, because only ONE of them is a chargeable failure:
//
//   * kConnected    — done; the state gate stops the chain.
//   * kRejected     — the COMAddIns Connect ran and was REJECTED. The only class
//                     a connect-failure budget may charge.
//   * kNoApp        — the Connect was SKIPPED: no Application object yet (Excel
//                     is empty). Not a failure; charged to the separate, much
//                     longer noApp budget (see the constants above).
//   * kNotAttempted — we returned WITHOUT EVER REACHING SetRibbonConnected: the
//                     unload bail, or the STA re-entrancy bail (an Excel callback
//                     dispatched while a Connect/bounce is pumping the message
//                     loop re-enters this function on the same thread and the
//                     s_inConnect CAS turns it away). Nothing was attempted, so
//                     NOTHING may be charged — this is the same accounting defect
//                     as the 2026-07-26 noApp fix, one branch further out: a
//                     queued OnTime macro dispatched mid-pump used to burn one of
//                     the 30 productive attempts without ever touching COM.
//                     Deliberately given NO budget of its own: it can only occur
//                     while an OUTER attempt is in flight on this same STA
//                     thread, so it is self-limiting — the outer call either
//                     resolves the state (chain stops) or returns a real,
//                     chargeable outcome that the next dispatch observes.
//
// The one-time registration failures (steps 1-3) also skip SetRibbonConnected but
// latch g_ribbonConnectState=2, so the caller's state gate short-circuits before
// the class is ever consulted; they report kNotAttempted for the same reason.
//
// enum class, so a caller cannot accidentally treat the class as a bool.
enum class RibbonAttempt {
    kNotAttempted = 0, // never reached the COMAddIns Connect — charge NOTHING
    kNoApp,            // Connect skipped: no Application object yet
    kRejected,         // Connect attempted and rejected — the chargeable class
    kConnected,        // Connect succeeded
};

// pOutcome (optional out): the attempt's class (above). Always written on every
// exit, so a caller never reads a stale value.
bool TryConnectRibbon(const char* phase, bool allowBounce = false,
                      RibbonAttempt* pOutcome = nullptr);

} // namespace ribbon
} // namespace xll
