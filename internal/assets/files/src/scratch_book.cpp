// scratch_book.cpp — see include/com/scratch_book.h for the contract, in
// particular why the suppression record is file-static rather than a member.
#include "com/scratch_book.h"

// Compiled only for ribbon builds, mirroring src/ribbon_addin.cpp: this TU is
// swept up by file(GLOB src/*.cpp) in every project, but the COM libraries it
// needs (oleaut32 for the VARIANT calls) are linked only when the ribbon is on.
#ifdef XLL_RIBBON_ENABLED

#include "com/dispatch_helpers.h"
#include "types/utility.h" // PascalToWString
#include "xll_excel.h"
#include "xll_log.h"

#include <string>

namespace xll {
namespace ribbon {

std::wstring GetActiveWorkbookName() {
    ScopedXLOPER12Result xName;
    if (xll::CallExcel(xlfGetDocument, xName, 88) != xlretSuccess) return std::wstring();
    if ((xName.get()->xltype & xltypeStr) == 0 || !xName.get()->val.str) return std::wstring();
    return PascalToWString(xName.get()->val.str);
}

namespace {

// The suppression record outlives the guard object on the SEH path, so it is
// file-static rather than a member. VT_BOOL VARIANTs own no resources, but
// VariantClear is load-bearing hygiene: a hostile IDispatch may have returned a
// non-BOOL (VT_DISPATCH/VT_BSTR) that failed the VT_BOOL arm check yet still
// owns resources.
struct PendingEventSuppression {
    IDispatch* app = nullptr; // AddRef'd while a suppression is pending
    VARIANT oldEnableEvents;
    VARIANT oldDisplayAlerts;
    bool armedEvents = false;
    bool armedAlerts = false;
};

PendingEventSuppression g_pendingSuppression; // STA-only access (bounce + xlAutoOpen)

} // namespace

void RestorePendingEventSuppression() {
    PendingEventSuppression& p = g_pendingSuppression;
    if (p.app) {
        if (p.armedEvents) {
            HRESULT hr = xll::com::Invoke(p.app, L"EnableEvents", DISPATCH_PROPERTYPUT, { p.oldEnableEvents }, nullptr);
            if (FAILED(hr)) SAFE_LOG_WARN("Ribbon bounce: failed to restore Application.EnableEvents after the scratch-book close (hr=" + std::to_string(hr) + "); Workbook events may stay suppressed for this session.");
        }
        if (p.armedAlerts) {
            HRESULT hr = xll::com::Invoke(p.app, L"DisplayAlerts", DISPATCH_PROPERTYPUT, { p.oldDisplayAlerts }, nullptr);
            if (FAILED(hr)) SAFE_LOG_WARN("Ribbon bounce: failed to restore Application.DisplayAlerts after the scratch-book close (hr=" + std::to_string(hr) + ").");
        }
        p.app->Release();
        p.app = nullptr;
    }
    p.armedEvents = false;
    p.armedAlerts = false;
    VariantClear(&p.oldEnableEvents);
    VariantClear(&p.oldDisplayAlerts);
}

ScratchCloseEventSuppressor::ScratchCloseEventSuppressor(IDispatch* a) {
    PendingEventSuppression& p = g_pendingSuppression;
    VariantInit(&p.oldEnableEvents);
    VariantInit(&p.oldDisplayAlerts);
    p.armedEvents = false;
    p.armedAlerts = false;
    if (!a) return; // no Application object -> nothing to suppress
    p.app = a;
    p.app->AddRef(); // the record outlives this frame on the SEH path
    VARIANT vFalse; VariantInit(&vFalse);
    vFalse.vt = VT_BOOL;
    vFalse.boolVal = VARIANT_FALSE;
    if (SUCCEEDED(xll::com::GetProperty(p.app, L"EnableEvents", &p.oldEnableEvents)) && p.oldEnableEvents.vt == VT_BOOL) {
        p.armedEvents = SUCCEEDED(xll::com::Invoke(p.app, L"EnableEvents", DISPATCH_PROPERTYPUT, { vFalse }, nullptr));
    }
    if (SUCCEEDED(xll::com::GetProperty(p.app, L"DisplayAlerts", &p.oldDisplayAlerts)) && p.oldDisplayAlerts.vt == VT_BOOL) {
        p.armedAlerts = SUCCEEDED(xll::com::Invoke(p.app, L"DisplayAlerts", DISPATCH_PROPERTYPUT, { vFalse }, nullptr));
    }
    if (!p.armedEvents && !p.armedAlerts) {
        // Nothing flipped: clear the record now so the belt-and-braces replay has
        // nothing to do (and the AddRef does not linger).
        RestorePendingEventSuppression();
    }
}

ScratchCloseEventSuppressor::~ScratchCloseEventSuppressor() { RestorePendingEventSuppression(); }

} // namespace ribbon
} // namespace xll

#endif // XLL_RIBBON_ENABLED
