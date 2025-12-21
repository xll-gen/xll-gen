#include "xll_path.h"
#include <windows.h>
#include <algorithm>

namespace xll {

    void ReplaceAll(std::wstring& str, const std::wstring& from, const std::wstring& to) {
        if(from.empty()) return;
        size_t start_pos = 0;
        while((start_pos = str.find(from, start_pos)) != std::wstring::npos) {
            str.replace(start_pos, from.length(), to);
            start_pos += to.length();
        }
    }

    bool FileExists(const std::wstring& path) {
        DWORD dwAttrib = GetFileAttributesW(path.c_str());
        return (dwAttrib != INVALID_FILE_ATTRIBUTES && !(dwAttrib & FILE_ATTRIBUTE_DIRECTORY));
    }
}
