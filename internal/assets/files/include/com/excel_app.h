#pragma once
// excel_app.h — shared, header-only acquisition of the hosting Excel's
// Application IDispatch via the in-process accessibility route.
//
// WHY THIS EXISTS: two unrelated features need the in-process Application
// object — the ribbon COMAddIns connect / RTD ThrottleInterval put (both gated
// behind `{{if .Ribbon.Enabled}}` / `{{if .Rtd...}}` in xll_main.cpp.tmpl) and
// the ALWAYS-ON date auto-format module (src/xll_date_format.cpp), which sets
// Range.NumberFormat directly so it never disturbs the user's active selection.
// The date module compiles in EVERY build (no XLL_RTD_ENABLED gate), so the
// acquisition logic cannot live only inside a template gate. It is factored here
// as header-only inline functions usable from both the template and the asset.
//
// The window walk + AccessibleObjectFromWindow(OBJID_NATIVEOM) route mirrors
// Excel-DNA (Source/ExcelDna.Integration/ExcelDnaUtil.cs, GetApplication) and
// xlOil. It returns the Application IDispatch AddRef'd; the CALLER releases it.
//
// THREADING: must run on Excel's main STA thread (xlAutoOpen / xlAutoClose / the
// calc-end deferred runner all qualify). Returns nullptr when the window chain
// or the native object model is not reachable yet (e.g. early-startup probe
// loads with no workbook open) — callers degrade gracefully, never crash.

#include <windows.h>
#include <ole2.h>
#include <oleacc.h>
#include "com/dispatch_helpers.h" // xll::com::GetProperty

namespace xll { namespace com {

namespace detail {

    // EnumChildWindows callback: stop at the first EXCEL7 child (the in-place
    // editing window that exposes OBJID_NATIVEOM).
    inline BOOL CALLBACK FindExcel7Proc(HWND hwnd, LPARAM lParam) {
        wchar_t cls[16] = {};
        GetClassNameW(hwnd, cls, 15);
        if (wcscmp(cls, L"EXCEL7") == 0) {
            *reinterpret_cast<HWND*>(lParam) = hwnd;
            return FALSE;
        }
        return TRUE;
    }

    // EnumThreadWindows callback: find this thread's top-level XLMAIN frame.
    inline BOOL CALLBACK FindXlMainProc(HWND hwnd, LPARAM lParam) {
        wchar_t cls[32] = {};
        GetClassNameW(hwnd, cls, 31);
        if (wcscmp(cls, L"XLMAIN") == 0) {
            *reinterpret_cast<HWND*>(lParam) = hwnd;
            return FALSE;
        }
        return TRUE;
    }

} // namespace detail

// Returns the hosting Excel's Application IDispatch (AddRef'd; caller releases),
// or nullptr if the window chain / native object model is not available yet.
// Route: XLMAIN (this thread's frame) -> XLDESK -> EXCEL7 child window ->
// AccessibleObjectFromWindow(OBJID_NATIVEOM) -> Window.Application. Never throws.
inline IDispatch* AcquireExcelApplication() {
    HWND frame = nullptr;
    EnumThreadWindows(GetCurrentThreadId(), detail::FindXlMainProc,
                      reinterpret_cast<LPARAM>(&frame));
    if (!frame) return nullptr;

    HWND excel7 = nullptr;
    HWND xldesk = FindWindowExW(frame, nullptr, L"XLDESK", nullptr);
    if (xldesk) excel7 = FindWindowExW(xldesk, nullptr, L"EXCEL7", nullptr);
    if (!excel7) {
        EnumChildWindows(frame, detail::FindExcel7Proc,
                         reinterpret_cast<LPARAM>(&excel7));
    }
    if (!excel7) return nullptr;

    IDispatch* pWindow = nullptr;
    if (FAILED(AccessibleObjectFromWindow(excel7, OBJID_NATIVEOM, IID_IDispatch,
                                          reinterpret_cast<void**>(&pWindow))) ||
        !pWindow) {
        return nullptr;
    }

    IDispatch* pApp = nullptr;
    VARIANT vApp; VariantInit(&vApp);
    if (SUCCEEDED(xll::com::GetProperty(pWindow, L"Application", &vApp)) &&
        vApp.vt == VT_DISPATCH && vApp.pdispVal) {
        pApp = vApp.pdispVal;
        pApp->AddRef();
    }
    VariantClear(&vApp);
    pWindow->Release();
    return pApp;
}

}} // namespace xll::com
