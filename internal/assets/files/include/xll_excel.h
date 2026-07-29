#pragma once
#include "types/ScopedXLOPER12.h"
#include <vector>
#include <tuple>

namespace xll {
    namespace detail {
        // Pass-through for LPXLOPER12
        inline LPXLOPER12 make_keeper(LPXLOPER12 p) { return p; }

        // Pass-through for nullptr
        inline LPXLOPER12 make_keeper(std::nullptr_t) { return nullptr; }

        // Pass-through for an existing ScopedXLOPER12 (caller owns its lifetime,
        // which outlives the CallExcel invocation). ScopedXLOPER12 is move-only,
        // so we cannot copy it into the keeper tuple; extract its pointer instead.
        // This makes passing a live ScopedXLOPER12 (e.g. xlfCaller's result) to a
        // subsequent Excel call work without taking its address.
        inline LPXLOPER12 make_keeper(ScopedXLOPER12& s) { return s.get(); }

        // Wrap everything else (int/double/bool/wchar_t* literals) in ScopedXLOPER12
        template <typename T>
        inline ScopedXLOPER12 make_keeper(T&& val) { return ScopedXLOPER12(std::forward<T>(val)); }

        // Get LPXLOPER12 from keeper
        inline LPXLOPER12 get_ptr(LPXLOPER12 p) { return p; }
        inline LPXLOPER12 get_ptr(ScopedXLOPER12& s) { return s; }
    }

    // Generic Excel Call Wrapper
    // Handles conversion of arguments to safe XLOPER12 pointers.
    // - Literals (int, double, bool, wchar_t*) are wrapped in ScopedXLOPER12.
    // - Existing LPXLOPER12 are passed through.
    template <typename... Args>
    int CallExcel(int xlfn, LPXLOPER12 res, Args&&... args) {
        // Create a tuple of "keepers" (either ScopedXLOPER12 or raw LPXLOPER12)
        auto keepers = std::make_tuple(detail::make_keeper(std::forward<Args>(args))...);

        // Extract pointers from keepers
        std::vector<LPXLOPER12> ptrs;
        ptrs.reserve(sizeof...(Args));

        std::apply([&](auto&... k) {
            (ptrs.push_back(detail::get_ptr(k)), ...);
        }, keepers);

        return Excel12v(xlfn, res, (int)ptrs.size(), ptrs.data());
    }

    // ReleaseOrTransferExcelResult settles ownership of an XLOPER12 that
    // Excel12/Excel12v FILLED IN (xlfRtd, xlCoerce, xlfGetCell, ...).
    //
    // WHY THIS EXISTS. The auxiliary memory behind xltypeStr / xltypeMulti /
    // xltypeRef in such a result belongs to EXCEL, and the XLL SDK gives exactly
    // two legal dispositions:
    //   * release it ourselves with xlFree, or
    //   * if the value is what our UDF RETURNS, set xlbitXLFree so Excel frees
    //     it after reading the result.
    // Doing NEITHER leaks Excel-heap memory on every call, with no trace in any
    // log. That is precisely what the plain-rtd and rtd-once wrappers did with
    // xlfRtd's result: plain rtd returned a static XLOPER12 carrying an
    // Excel-allocated Pascal string with no ownership bit, and rtd-once threw
    // the same value away with `(void)xRes;`. A one-second RTD clock pushing
    // strings accumulated one buffer per cell per tick, forever. `types` v0.2.9
    // fixed the same contract violation for the OTHER Excel12 result sites (see
    // ScopedXLOPER12Result's Free() comment in types/ScopedXLOPER12.h); these
    // two wrappers were simply missed, which is why the fix lives here as ONE
    // helper both of them call instead of two hand-written copies.
    //
    // transferToExcel == true  -> the caller RETURNS op to Excel: set
    //   xlbitXLFree and free nothing. This is safe on a `static`/`thread_local`
    //   XLOPER12, which is what both wrappers use: xlbitXLFree asks Excel to
    //   release the memory the XLOPER12 POINTS AT, never the XLOPER12 struct
    //   itself (that stays ours, and Excel has no way to free it — it did not
    //   allocate it). The bit that WOULD hand over the struct is xlbitDLLFree,
    //   which routes to our exported xlAutoFree12 instead; the two are
    //   deliberately not interchangeable. The struct is reused on the next call,
    //   by which point Excel is done with the value it already returned to the
    //   cell, and xlfRtd overwrites the whole operand anyway.
    // transferToExcel == false -> the caller DISCARDS op: release it NOW with
    //   xlFree and reset the operand to xltypeNil, so a later inspection cannot
    //   see a dangling pointer and a repeated call cannot double-free.
    //
    // Scalar results (Num/Int/Bool/Err/Nil/Missing) own no Excel-side memory, so
    // both branches are no-ops for them: no pointless C-API round-trip, and no
    // ownership bit on a value that has nothing to own.
    //
    // A result already tagged xlbitDLLFree is OURS (a pool node / NewExcelString),
    // never an Excel12 output; it is left untouched, because xlFree would corrupt
    // our allocator and xlbitXLFree would ask Excel to free memory it never gave
    // us. That combination should not arise on these paths, but the operand is
    // reused across calls, so the guard keeps the two ownership schemes from ever
    // being mixed on one XLOPER12.
    inline void ReleaseOrTransferExcelResult(XLOPER12& op, bool transferToExcel) {
        switch (op.xltype & ~(xlbitXLFree | xlbitDLLFree)) {
        case xltypeStr:
        case xltypeMulti:
        case xltypeRef:
            break;
        default:
            return; // nothing Excel allocated
        }

        if ((op.xltype & xlbitDLLFree) != 0) {
            return; // DLL-owned payload: not ours to hand to xlFree/Excel
        }

        if (transferToExcel) {
            op.xltype |= xlbitXLFree;
            return;
        }

        CallExcel(xlFree, nullptr, &op);
        op.xltype = xltypeNil;
    }
}
