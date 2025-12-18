#include "xll_utility.h"
#include "xll_mem.h"
#include <vector>

std::wstring StringToWString(const std::string& str) {
    if (str.empty()) return std::wstring();
    int size_needed = MultiByteToWideChar(CP_UTF8, 0, &str[0], (int)str.size(), NULL, 0);
    std::wstring wstrTo(size_needed, 0);
    MultiByteToWideChar(CP_UTF8, 0, &str[0], (int)str.size(), &wstrTo[0], size_needed);
    return wstrTo;
}

std::wstring ConvertToWString(const char* str) {
    if (!str) return std::wstring();
    std::string s(str);
    return StringToWString(s);
}

std::string WideToUtf8(const std::wstring& wstr) {
    if (wstr.empty()) return "";
    int size_needed = WideCharToMultiByte(CP_UTF8, 0, &wstr[0], (int)wstr.size(), NULL, 0, NULL, NULL);
    std::string strTo(size_needed, 0);
    WideCharToMultiByte(CP_UTF8, 0, &wstr[0], (int)wstr.size(), &strTo[0], size_needed, NULL, NULL);
    return strTo;
}

LPXLOPER12 TempStr12(const wchar_t* txt) {
    static thread_local XLOPER12 xOp[10];
    static thread_local int i = 0;
    i = (i + 1) % 10;
    LPXLOPER12 op = &xOp[i];

    op->xltype = xltypeStr;
    static thread_local wchar_t strBuf[10][256];
    size_t len = 0;
    if (txt) len = wcslen(txt);
    if (len > 255) len = 255;

    strBuf[i][0] = (wchar_t)len;
    if (len > 0) wmemcpy(strBuf[i]+1, txt, len);

    op->val.str = strBuf[i];
    return op;
}

LPXLOPER12 TempInt12(int val) {
    static thread_local XLOPER12 xOp[10];
    static thread_local int i = 0;
    i = (i + 1) % 10;
    LPXLOPER12 op = &xOp[i];
    op->xltype = xltypeInt;
    op->val.w = val;
    return op;
}

std::string ConvertExcelString(const wchar_t* wstr) {
    if (!wstr) return "";
    size_t len = (size_t)wstr[0]; // Pascal string length
    if (len == 0) return "";
    std::wstring ws(wstr + 1, len);
    return WideToUtf8(ws);
}

bool IsSingleCell(LPXLOPER12 pxRef) {
    if (!pxRef) return false;
    if (pxRef->xltype & xltypeSRef) {
        int h = pxRef->val.sref.ref.rwLast - pxRef->val.sref.ref.rwFirst + 1;
        int w = pxRef->val.sref.ref.colLast - pxRef->val.sref.ref.colFirst + 1;
        return (h == 1 && w == 1);
    }
    if (pxRef->xltype & xltypeRef) {
        // Multi-area reference
        // Check if only 1 area and it is 1x1
        if (pxRef->val.mref.lpmref->count == 1) {
            const auto& r = pxRef->val.mref.lpmref->reftbl[0];
            int h = r.rwLast - r.rwFirst + 1;
            int w = r.colLast - r.colFirst + 1;
            return (h == 1 && w == 1);
        }
    }
    return false;
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
