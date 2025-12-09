#include "xll_embed.h"
#include <windows.h>
#include <string>
#include <vector>
#include <fstream>
#include <iostream>
#include <zstd.h>
#include <process.h>

// Resource ID defined in resource.rc (assumed to be 101 or similar, but we need the actual ID)
// For this template, we will assume a fixed ID. The RC generation must match this.
#define IDR_GO_ZST 101

extern HMODULE g_hModule;

namespace embed {

// Helper to expand environment variables like %TEMP% or ${TEMP}
std::string ExpandEnvVars(const std::string& pattern) {
    std::string p = pattern;
    // Replace ${VAR} with %VAR% for Windows API compatibility
    size_t start_pos = 0;
    while((start_pos = p.find("${", start_pos)) != std::string::npos) {
        size_t end_pos = p.find("}", start_pos);
        if (end_pos != std::string::npos) {
            p.replace(end_pos, 1, "%");
            p.replace(start_pos, 2, "%");
            start_pos += 1;
        } else {
            break;
        }
    }

    char buffer[MAX_PATH];
    DWORD res = ExpandEnvironmentStringsA(p.c_str(), buffer, MAX_PATH);
    if (res == 0 || res > MAX_PATH) {
        return pattern; // Fallback or empty?
    }
    return std::string(buffer);
}

bool DecompressAndWrite(void* pSrc, size_t srcSize, const std::string& destPath) {
    unsigned long long dstSize = ZSTD_getFrameContentSize(pSrc, srcSize);
    if (dstSize == ZSTD_CONTENTSIZE_ERROR || dstSize == ZSTD_CONTENTSIZE_UNKNOWN) {
        return false;
    }

    std::vector<char> dstBuffer(dstSize);
    size_t decompressedSize = ZSTD_decompress(dstBuffer.data(), dstSize, pSrc, srcSize);

    if (ZSTD_isError(decompressedSize)) {
        return false;
    }

    // Use CreateFile to set FILE_ATTRIBUTE_TEMPORARY | FILE_FLAG_DELETE_ON_CLOSE
    // Note: To make DELETE_ON_CLOSE work, we must keep the handle open,
    // OR allow sharing such that CreateProcess can open it.
    // However, if we CloseHandle immediately, the file is deleted.
    // So for "simple" execution where we don't manage the process handle lifetime deeply,
    // using FILE_ATTRIBUTE_TEMPORARY + unique name is safer and simpler.
    // We omit DELETE_ON_CLOSE here to ensure the file exists for CreateProcess.
    // Cleanup is handled by OS (eventually) or unique naming avoids conflict.

    HANDLE hFile = CreateFileA(destPath.c_str(), GENERIC_WRITE, 0, NULL, CREATE_ALWAYS,
                               FILE_ATTRIBUTE_NORMAL | FILE_ATTRIBUTE_TEMPORARY, NULL);
    if (hFile == INVALID_HANDLE_VALUE) {
        return false;
    }

    DWORD written;
    BOOL ok = WriteFile(hFile, dstBuffer.data(), (DWORD)decompressedSize, &written, NULL);
    CloseHandle(hFile);

    return ok != 0;
}

std::string ExtractAndStartExe(const std::string& tempDirPattern) {
    std::string tempDir = ExpandEnvVars(tempDirPattern);

    // Ensure trailing slash
    if (!tempDir.empty() && tempDir.back() != '\\' && tempDir.back() != '/') {
        tempDir += "\\";
    }

    // Use PID to prevent collision
    std::string exeName = "embedded_server_" + std::to_string(GetCurrentProcessId()) + ".exe";
    std::string exePath = tempDir + exeName;

    // Check if we need to extract.
    // Optimization: Check if file exists. For now, we always overwrite to ensure version match
    // unless we implement hash checking. But given the "performance" discussion,
    // we use FILE_ATTRIBUTE_TEMPORARY which is fast.

    HRSRC hRes = FindResourceA(g_hModule, MAKEINTRESOURCEA(IDR_GO_ZST), RT_RCDATA);
    if (!hRes) {
        // Resource not found?
        return "";
    }

    HGLOBAL hMem = LoadResource(g_hModule, hRes);
    void* pData = LockResource(hMem);
    DWORD size = SizeofResource(g_hModule, hRes);

    if (!pData || size == 0) {
        return "";
    }

    if (!DecompressAndWrite(pData, size, exePath)) {
        return "";
    }

    return exePath;
}

} // namespace embed
