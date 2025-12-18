#ifndef PASCAL_STRING_H
#define PASCAL_STRING_H

#include <string>
#include <vector>

// Converts a C-style string to a Pascal-style string (byte-length-prefixed).
// The resulting string is null-terminated (though Pascal strings don't strictly require it, Excel often likes it).
std::vector<char> CStringToPascalString(const std::string& c_str);

// Converts a Pascal-style string (byte-length-prefixed) to a C-style string.
std::string PascalStringToCString(const unsigned short* pascal_str);

// Converts a wide string to an Excel 12 Pascal-style string (length-prefixed wide string).
// In Excel 12, strings are wchar_t*, where the first character is the length (0-32767).
// The resulting vector contains the length at index 0, followed by the characters.
std::vector<wchar_t> WStringToPascalString(const std::wstring& w_str);

// Converts an Excel 12 Pascal-style string to a standard wide string.
std::wstring PascalString12ToWString(const wchar_t* pascal_str);

// Alias for PascalString12ToWString
std::wstring PascalToWString(const wchar_t* pascal_str);

// Creates a new Pascal string on the heap (caller must free or manage).
wchar_t* WStringToNewPascalString(const std::wstring& w_str);

#endif // PASCAL_STRING_H
