#include "PascalString.h"
#include <vector>
#include <string>
#include <algorithm> // For std::min

// Converts a C-style string to a Pascal-style string (length-prefixed).
std::vector<char> CStringToPascalString(const std::string& c_str) {
    unsigned char length = static_cast<unsigned char>(std::min((size_t)255, c_str.length()));
    std::vector<char> pascal_str(length + 2);

    pascal_str[0] = static_cast<char>(length);
    if (length > 0) {
        std::copy(c_str.begin(), c_str.begin() + length, pascal_str.begin() + 1);
    }
    pascal_str[length + 1] = '\0';

    return pascal_str;
}

std::string PascalStringToCString(const unsigned short* pascal_str) {
    if (!pascal_str) {
        return "";
    }
    unsigned short length = pascal_str[0];
    std::string result;
    result.reserve(length);
    for (unsigned short i = 0; i < length; ++i) {
        result.push_back(static_cast<char>(pascal_str[i + 1]));
    }
    return result;
}

std::vector<wchar_t> WStringToPascalString(const std::wstring& w_str) {
    unsigned short length = static_cast<unsigned short>(std::min((size_t)32767, w_str.length()));
    std::vector<wchar_t> pascal_str(length + 2);

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

std::wstring PascalToWString(const wchar_t* pascal_str) {
    return PascalString12ToWString(pascal_str);
}

wchar_t* WStringToNewPascalString(const std::wstring& w_str) {
    auto vec = WStringToPascalString(w_str);
    wchar_t* buf = new wchar_t[vec.size()];
    std::copy(vec.begin(), vec.end(), buf);
    return buf;
}
