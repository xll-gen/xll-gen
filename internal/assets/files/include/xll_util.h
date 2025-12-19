#pragma once
#include <string>

namespace xll {

    // Helper to replace all occurrences of a substring
    static inline void ReplaceAll(std::wstring& str, const std::wstring& from, const std::wstring& to) {
        if(from.empty()) return;
        size_t start_pos = 0;
        while((start_pos = str.find(from, start_pos)) != std::wstring::npos) {
            str.replace(start_pos, from.length(), to);
            start_pos += to.length();
        }
    }

}
