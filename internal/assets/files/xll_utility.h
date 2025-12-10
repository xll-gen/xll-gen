#pragma once
#include <windows.h>
#include <string>
#include <vector>
#include "xlcall.h"

typedef wchar_t XLL_PASCAL_STRING;

// Global Module Handle (extern)
extern HINSTANCE g_hModule;

// Registration Helpers
LPXLOPER12 TempStr12(const wchar_t* txt);
LPXLOPER12 TempInt12(int val);

// String Conversion Helpers
std::wstring StringToWString(const std::string& str);
std::string WideToUtf8(const std::wstring& wstr);
const char* ConvertExcelString(const wchar_t* wstr);

// Path Helper
std::wstring GetXllDir();
