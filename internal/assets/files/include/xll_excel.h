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
}
