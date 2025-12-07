#include <windows.h>
#include <wchar.h>
#include <stdio.h>
// Ensure xlcall.h is in your include path (e.g., from Excel SDK or generated/cpp/include)
#include "xlcall.h"

// Global module handle
HINSTANCE g_hModule = NULL;

BOOL WINAPI DllMain(HINSTANCE hinstDLL, DWORD fdwReason, LPVOID lpvReserved) {
    if (fdwReason == DLL_PROCESS_ATTACH) {
        g_hModule = hinstDLL;
        DisableThreadLibraryCalls(hinstDLL);
    }
    return TRUE;
}

// Memory management for return values
// We allocate XLOPER12 and string buffer on heap to be safe and simple.
LPXLOPER12 NewExcelString(const wchar_t* txt) {
    size_t len = txt ? wcslen(txt) : 0;
    if (len > 32767) len = 32767;

    LPXLOPER12 op = (LPXLOPER12)malloc(sizeof(XLOPER12));
    if (!op) return NULL;

    op->xltype = xltypeStr | xlbitDLLFree;
    op->val.str = (wchar_t*)malloc((len + 1) * sizeof(wchar_t));
    if (!op->val.str) {
        free(op);
        return NULL;
    }

    op->val.str[0] = (wchar_t)len;
    if (len > 0) wmemcpy(op->val.str + 1, txt, len);

    return op;
}

extern "C" __declspec(dllexport) void __stdcall xlAutoFree12(LPXLOPER12 pxFree) {
    if (pxFree && (pxFree->xltype & xlbitDLLFree)) {
        if ((pxFree->xltype & xltypeMulti) == 0) {
             if ((pxFree->xltype & ~xlbitDLLFree) == xltypeStr && pxFree->val.str) {
                 free(pxFree->val.str);
             }
        }
        free(pxFree);
    }
}

// Helper for registration (Ring buffer)
LPXLOPER12 TempStr12(const wchar_t* txt) {
    static XLOPER12 xOp[10];
    static int i = 0;
    i = (i + 1) % 10;
    LPXLOPER12 op = &xOp[i];

    op->xltype = xltypeStr;
    static wchar_t strBuf[10][256];
    size_t len = 0;
    if (txt) len = wcslen(txt);
    if (len > 255) len = 255;

    strBuf[i][0] = (wchar_t)len;
    if (len > 0) wmemcpy(strBuf[i]+1, txt, len);

    op->val.str = strBuf[i];
    return op;
}

extern "C" __declspec(dllexport) int __stdcall xlAutoOpen() {
    static XLOPER12 xDll;
    Excel12(xlGetName, &xDll, 0);

    Excel12(xlfRegister, 0, 11,
        &xDll,
        TempStr12(L"ProbeString"),
        TempStr12(L"QD%$"), // Return Q (String), Arg D% (String Ptr), ThreadSafe
        TempStr12(L"ProbeString"),
        TempStr12(L"s"),
        TempStr12(L"1"),
        TempStr12(L"ProbeExperiment"),
        TempStr12(L""),
        TempStr12(L""),
        TempStr12(L"Returns the pointer address of the input string argument"),
        TempStr12(L"s (D%)")
    );

    Excel12(xlFree, 0, 1, &xDll);
    return 1;
}

extern "C" __declspec(dllexport) int __stdcall xlAutoClose() {
    return 1;
}

extern "C" __declspec(dllexport) LPXLOPER12 __stdcall ProbeString(const wchar_t* s) {
    wchar_t buf[64];
    // Format pointer address to hex string
    #ifdef _MSC_VER
    _snwprintf_s(buf, 64, _TRUNCATE, L"0x%p", s);
    #else
    swprintf(buf, 64, L"0x%p", s);
    #endif

    return NewExcelString(buf);
}
