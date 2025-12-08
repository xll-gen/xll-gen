#ifndef XLL_UTILITY_H
#define XLL_UTILITY_H

#include <windows.h>
#include <string>
#include <vector>
#include <thread>
#include <sstream>
#include "xlcall.h"

// Forward declaration of g_hModule from xll_main.cpp or where it's defined
// Actually, it's better to declare it here as extern if it's used across files,
// but since we are extracting GetXllDir, we can move g_hModule logic or pass it.
// However, DllMain is usually in the main file.
// Let's declare the functions.

extern HINSTANCE g_hModule;

// String Conversion
std::wstring StringToWString(const std::string& str);
std::string WideToUtf8(const std::wstring& wstr);
const char* ConvertExcelString(const wchar_t* wstr);

// Excel Helpers
LPXLOPER12 TempStr12(const wchar_t* txt);
LPXLOPER12 TempInt12(int val);
LPXLOPER12 NewExcelString(const wchar_t* wstr);
std::wstring GetSheetName(LPXLOPER12 pxRef);
std::wstring GetXllDir();

#endif // XLL_UTILITY_H
