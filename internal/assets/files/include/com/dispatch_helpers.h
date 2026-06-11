#pragma once
// Minimal late-bound IDispatch helpers for the ribbon add-in connect path.
// Not a general automation layer — that is sugar's job on the Go side.
#include <windows.h>
#include <ole2.h>
#include <string>
#include <vector>

namespace xll { namespace com {

    inline HRESULT GetDispId(IDispatch* disp, const wchar_t* name, DISPID* out) {
        if (!disp) return E_POINTER;
        LPOLESTR n = const_cast<LPOLESTR>(name);
        return disp->GetIDsOfNames(IID_NULL, &n, 1, LOCALE_USER_DEFAULT, out);
    }

    // Invoke flags: DISPATCH_PROPERTYGET, DISPATCH_PROPERTYPUT or DISPATCH_METHOD.
    // Args are passed in natural order and reversed internally per IDispatch ABI.
    // Late-bound invoke. Args are borrowed [in] parameters: this function
    // copies VARIANTs shallowly and never clears them — the CALLER retains
    // ownership and must free contained BSTR/IDispatch* after the call.
    // For property puts (e.g. Connect = VARIANT_TRUE) pass
    // DISPATCH_PROPERTYPUT; the DISPID_PROPERTYPUT named arg is set up here.
    inline HRESULT Invoke(IDispatch* disp, const wchar_t* name, WORD flags,
                          std::vector<VARIANT> args, VARIANT* result) {
        DISPID dispid;
        HRESULT hr = GetDispId(disp, name, &dispid);
        if (FAILED(hr)) return hr;

        std::vector<VARIANT> reversed(args.rbegin(), args.rend());
        DISPPARAMS dp{};
        dp.cArgs = static_cast<UINT>(reversed.size());
        dp.rgvarg = reversed.empty() ? nullptr : reversed.data();

        DISPID putid = DISPID_PROPERTYPUT;
        if (flags & DISPATCH_PROPERTYPUT) {
            dp.cNamedArgs = 1;
            dp.rgdispidNamedArgs = &putid;
        }
        return disp->Invoke(dispid, IID_NULL, LOCALE_USER_DEFAULT, flags, &dp, result, nullptr, nullptr);
    }

    inline HRESULT GetProperty(IDispatch* disp, const wchar_t* name, VARIANT* result) {
        return Invoke(disp, name, DISPATCH_PROPERTYGET, {}, result);
    }

}} // namespace xll::com
