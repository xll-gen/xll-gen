#include "PascalString.h"
#include <vector>
#include <string>
#include <algorithm> // For std::min

// Converts a C-style string to a Pascal-style string (length-prefixed).
// The resulting string is null-terminated.
// Excel's Pascal strings (legacy) are typically limited to 255 characters.
std::vector<char> CStringToPascalString(const std::string& c_str) {
    // Excel Pascal strings limit the length to 255.
    unsigned char length = static_cast<unsigned char>(std::min((size_t)255, c_str.length()));
    std::vector<char> pascal_str(length + 2); // +1 for length, +1 for null terminator

    pascal_str[0] = static_cast<char>(length);
    if (length > 0) {
        std::copy(c_str.begin(), c_str.begin() + length, pascal_str.begin() + 1);
    }
    pascal_str[length + 1] = '\0'; // Null terminate just in case

    return pascal_str;
}

// Converts a Pascal-style string (length-prefixed) to a C-style string.
// Assumes the input is a wide character Pascal string (unsigned short*), typical for Excel12.
// Note: This matches the user provided signature, which seems to mix types (short* for legacy string?)
// Actually, legacy Pascal strings are char*. Excel 12 strings are wchar_t*.
// The user provided code:
// std::string PascalStringToCString(const unsigned short* pascal_str) { ... return string ... }
// This implies converting Wide Pascal String to std::string (narrow).
std::string PascalStringToCString(const unsigned short* pascal_str) {
    if (!pascal_str) {
        return "";
    }
    // The first unsigned short contains the length of the string
    unsigned short length = pascal_str[0];
    // The actual string data starts from the second unsigned short.
    // We construct a narrow string from wide chars (simple cast, lossy for non-ASCII).
    // For proper conversion, WideCharToMultiByte should be used, but keeping it simple/matching intent.
    std::string result;
    result.reserve(length);
    for (unsigned short i = 0; i < length; ++i) {
        result.push_back(static_cast<char>(pascal_str[i + 1]));
    }
    return result;
}

// Converts a wide string to an Excel 12 Pascal-style string (length-prefixed wide string).
std::vector<wchar_t> WStringToPascalString(const std::wstring& w_str) {
    // Excel 12 strings limit the length to 32767 characters.
    unsigned short length = static_cast<unsigned short>(std::min((size_t)32767, w_str.length()));
    std::vector<wchar_t> pascal_str(length + 2); // +1 for length, +1 for null terminator

    pascal_str[0] = static_cast<wchar_t>(length);
    if (length > 0) {
        std::copy(w_str.begin(), w_str.begin() + length, pascal_str.begin() + 1);
    }
    pascal_str[length + 1] = L'\0';

    return pascal_str;
}

std::wstring PascalString12ToWString(const wchar_t* pascal_str) {
    if (!pascal_str) {
        return L"";
    }
    unsigned short length = static_cast<unsigned short>(pascal_str[0]);
    return std::wstring(pascal_str + 1, length);
}
