#pragma once
#include <string>

namespace xll {
    // Replaces all occurrences of `from` with `to` in `str`
    void ReplaceAll(std::wstring& str, const std::wstring& from, const std::wstring& to);

    // Checks if a file exists (and is not a directory)
    bool FileExists(const std::wstring& path);
}
