#include "xll_utility.h"
#include <vector>
#include <mutex>
#include <random>
#include "xll_mem.h"

// Defined in xll_main.cpp usually, but we need it here.
// For now, let's assume the user of this library will define it, or we move it here?
// DllMain sets it.
// We can define a getter/setter if we want to be clean, or just extern.
// For simplicity in refactoring:
// We will rely on extern.
// But wait, if I put it in xll_utility.cpp, I need to make sure there's only one definition.
// g_hModule is in xll_main.cpp.

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

LPXLOPER12 TempInt12(int val) {
    static XLOPER12 xOp[10];
    static int i = 0;
    i = (i + 1) % 10;
    LPXLOPER12 op = &xOp[i];
    op->xltype = xltypeInt;
    op->val.w = val;
    return op;
}

LPXLOPER12 NewExcelString(const wchar_t* wstr) {
    if (!wstr) return NULL;
    size_t len = wcslen(wstr);
    if (len > 32767) len = 32767;

    LPXLOPER12 op = (LPXLOPER12)XLL_MALLOC(sizeof(XLOPER12));
    if (!op) return NULL; // Should not happen

    op->xltype = xltypeStr | xlbitDLLFree;
    op->val.str = (wchar_t*)XLL_MALLOC((len + 2) * sizeof(wchar_t));
    if (!op->val.str) {
        XLL_FREE(op);
        return NULL;
    }

    op->val.str[0] = (wchar_t)len;
    if (len > 0) wmemcpy(op->val.str + 1, wstr, len);
    op->val.str[len + 1] = 0; // Null terminate just in case, though Excel counts it

    return op;
}

std::wstring StringToWString(const std::string& str) {
    if (str.empty()) return std::wstring();
    int size_needed = MultiByteToWideChar(CP_UTF8, 0, &str[0], (int)str.size(), NULL, 0);
    std::wstring wstrTo(size_needed, 0);
    MultiByteToWideChar(CP_UTF8, 0, &str[0], (int)str.size(), &wstrTo[0], size_needed);
    return wstrTo;
}

std::string WideToUtf8(const std::wstring& wstr) {
    if (wstr.empty()) return "";
    int size_needed = WideCharToMultiByte(CP_UTF8, 0, &wstr[0], (int)wstr.size(), NULL, 0, NULL, NULL);
    std::string strTo(size_needed, 0);
    WideCharToMultiByte(CP_UTF8, 0, &wstr[0], (int)wstr.size(), &strTo[0], size_needed, NULL, NULL);
    return strTo;
}

thread_local std::vector<char> g_strBuf;

const char* ConvertExcelString(const wchar_t* wstr) {
    if (!wstr) return "";
    size_t len = (size_t)wstr[0]; // Pascal string length
    if (len == 0) return "";

    // Ensure buffer space (UTF-8 max expansion is 4x)
    size_t cap = len * 4 + 1;
    if (g_strBuf.size() < cap) g_strBuf.resize(cap);

    int n = WideCharToMultiByte(CP_UTF8, 0, wstr + 1, (int)len, g_strBuf.data(), (int)g_strBuf.size(), NULL, NULL);
    if (n >= 0) g_strBuf[n] = '\0';
    else g_strBuf[0] = '\0';

    return g_strBuf.data();
}

std::wstring GetSheetName(LPXLOPER12 pxRef) {
    if (!pxRef || (pxRef->xltype != xltypeRef && pxRef->xltype != xltypeSRef)) {
        return L"";
    }

    XLOPER12 xRes;
    int ret = Excel12(xlSheetNm, &xRes, 1, pxRef);
    if (ret != xlretSuccess) return L"";

    std::wstring result;
    if (xRes.xltype == xltypeStr && xRes.val.str) {
         size_t len = (size_t)xRes.val.str[0];
         if (len > 0) {
             result.assign(xRes.val.str + 1, len);
         }
    }
    Excel12(xlFree, 0, 1, &xRes);
    return result;
}

std::wstring GetXllDir() {
    wchar_t path[MAX_PATH];
    if (GetModuleFileNameW(g_hModule, path, MAX_PATH) == 0) return L"";
    std::wstring p(path);
    size_t pos = p.find_last_of(L"\\/");
    if (pos != std::wstring::npos) {
        return p.substr(0, pos);
    }
    return L".";
}
