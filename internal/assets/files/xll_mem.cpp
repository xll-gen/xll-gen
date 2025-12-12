#include "xll_mem.h"
#include "ObjectPool.h"
#include "PascalString.h"
#include <mutex>
#include <vector>
#include <cstring> // For memset, memcpy

static ObjectPool<XLOPER12> xloperPool;

LPXLOPER12 NewXLOPER12() {
    LPXLOPER12 p = xloperPool.Acquire();
    // Initialize to zero.
    std::memset(p, 0, sizeof(XLOPER12));
    return p;
}

void ReleaseXLOPER12(LPXLOPER12 p) {
    if (p) {
        xloperPool.Release(p);
    }
}

LPXLOPER12 NewExcelString(const std::wstring& str) {
    LPXLOPER12 p = NewXLOPER12();
    p->xltype = xltypeStr | xlbitDLLFree;

    std::vector<wchar_t> pascalStr = WStringToPascalString(str);

    // Allocate buffer for the string.
    wchar_t* buffer = new wchar_t[pascalStr.size()];
    std::memcpy(buffer, pascalStr.data(), pascalStr.size() * sizeof(wchar_t));

    p->val.str = buffer;
    return p;
}

// Thread-local storage for FP12 return values
struct FP12Buffer {
    std::vector<char> data; // Stores FP12 header + doubles
};

// Keep a few buffers per thread to allow safe returns without immediate overwrites
static const int kRingBufferSize = 8;
thread_local int fpRingIndex = 0;
thread_local FP12Buffer fpRingBuffers[kRingBufferSize];

FP12* NewFP12(int rows, int cols) {
    FP12Buffer& buf = fpRingBuffers[fpRingIndex];
    fpRingIndex = (fpRingIndex + 1) % kRingBufferSize;

    // Calculate required size: 2 ints + rows*cols doubles
    size_t size = sizeof(INT32) * 2 + (size_t)rows * cols * sizeof(double);
    if (buf.data.size() < size) {
        buf.data.resize(size);
    }

    FP12* fp = reinterpret_cast<FP12*>(buf.data.data());
    fp->rows = rows;
    fp->columns = cols;
    return fp;
}

extern "C" void __stdcall xlAutoFree12(LPXLOPER12 p) {
    if (!p) return;

    // Check if the XLOPER12 itself is marked for DLL freeing
    // (Usually this function is only called if xlbitDLLFree is set on p->xltype)

    if (p->xltype & xltypeStr) {
        if (p->val.str) {
            delete[] p->val.str;
            p->val.str = nullptr;
        }
    }
    else if (p->xltype & xltypeMulti) {
         if (p->val.array.lparray) {
             int count = p->val.array.rows * p->val.array.columns;
             // We need to check if elements have allocated memory
             // Note: This assumes we constructed the array.
             // If we construct arrays, we should probably set xlbitDLLFree on string elements?
             // But usually for xltypeMulti, we just clean up what we own.
             for(int i=0; i<count; ++i) {
                 LPXLOPER12 elem = &p->val.array.lparray[i];
                 if ((elem->xltype & xltypeStr) && elem->val.str) {
                      // Only delete if we assume we own it.
                      // If we are consistent, we always allocate strings for return via new[]
                      delete[] elem->val.str;
                 }
             }
             delete[] p->val.array.lparray;
         }
    }

    // Finally, release the XLOPER12 struct itself back to the pool
    xloperPool.Release(p);
}
