#pragma once
#include <windows.h>
#include "xlcall.h"
#include <string>

// Allocates an XLOPER12 from the thread-safe object pool and initializes it to empty.
LPXLOPER12 NewXLOPER12();

// Creates an XLOPER12 String (Pascal-style wide string) managed by the DLL.
// Sets xltypeStr | xlbitDLLFree.
// The returned pointer and the string buffer are both managed and will be freed by xlAutoFree12.
LPXLOPER12 NewExcelString(const std::wstring& str);

// Creates an FP12 array managed by a ring buffer (valid for return to Excel).
// Note: FP12 is used with "K%" type. Excel copies the data, so it only needs to persist until return.
FP12* NewFP12(int rows, int cols);

// Callback called by Excel to free memory allocated by the XLL.
extern "C" void __stdcall xlAutoFree12(LPXLOPER12 p);
