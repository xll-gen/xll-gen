#include "xll_embed.h"
#include <windows.h>
#include <string>
#include <vector>
#include <fstream>
#include <iostream>
#include <zstd.h>
#include <process.h>
#include <sstream>
#include <iomanip>
#include <cstdint>

// Resource ID defined in resource.rc (assumed to be 101)
#define IDR_GO_ZST 101

extern HMODULE g_hModule;

namespace embed {

// FNV-1a Hash for Checksum
uint32_t FNV1a(const void* data, size_t size) {
    uint32_t hash = 2166136261u;
    const uint8_t* p = static_cast<const uint8_t*>(data);
    for (size_t i = 0; i < size; ++i) {
        hash ^= p[i];
        hash *= 16777619u;
    }
    return hash;
}

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

    // Use CreateFile without FILE_ATTRIBUTE_TEMPORARY to ensure persistence (cache)
    HANDLE hFile = CreateFileA(destPath.c_str(), GENERIC_WRITE, 0, NULL, CREATE_ALWAYS,
                               FILE_ATTRIBUTE_NORMAL, NULL);
    if (hFile == INVALID_HANDLE_VALUE) {
        return false;
    }

    DWORD written;
    BOOL ok = WriteFile(hFile, dstBuffer.data(), (DWORD)decompressedSize, &written, NULL);
    CloseHandle(hFile);

    return ok != 0;
}

std::string ExtractEmbeddedExe(const std::string& tempDirPattern, const std::string& projectName) {
    // 1. Check for Debug Override
    char envBuf[16];
    if (GetEnvironmentVariableA("XLL_DEV_DISABLE_EMBED", envBuf, 16) > 0) {
        if (std::string(envBuf) == "1") {
            return ""; // Trigger fallback
        }
    }

    // 2. Load Resource
    HRSRC hRes = FindResourceA(g_hModule, MAKEINTRESOURCEA(IDR_GO_ZST), RT_RCDATA);
    if (!hRes) return "";

    HGLOBAL hMem = LoadResource(g_hModule, hRes);
    void* pData = LockResource(hMem);
    DWORD size = SizeofResource(g_hModule, hRes);
    if (!pData || size == 0) return "";

    // 3. Compute Checksum
    uint32_t hash = FNV1a(pData, size);
    std::stringstream ss;
    ss << std::hex << std::setw(8) << std::setfill('0') << hash;
    std::string hashStr = ss.str();

    // 4. Resolve Directory
    std::string baseTemp = ExpandEnvVars(tempDirPattern);
    if (baseTemp.empty()) return "";
    if (baseTemp.back() != '\\' && baseTemp.back() != '/') baseTemp += "\\";

    std::string projectDir = baseTemp + projectName + "\\";
    CreateDirectoryA(projectDir.c_str(), NULL); // Ensure directory exists

    std::string exeName = projectName + "_" + hashStr + ".exe";
    std::string finalPath = projectDir + exeName;

    // 5. Check Cache
    DWORD attrs = GetFileAttributesA(finalPath.c_str());
    if (attrs != INVALID_FILE_ATTRIBUTES && !(attrs & FILE_ATTRIBUTE_DIRECTORY)) {
        // Cache Hit: File exists
        return finalPath;
    }

    // 6. Cache Miss: Extract to Temp and Atomic Rename
    std::string tempPath = finalPath + ".tmp_" + std::to_string(GetCurrentProcessId());

    if (DecompressAndWrite(pData, size, tempPath)) {
        if (MoveFileA(tempPath.c_str(), finalPath.c_str())) {
            // Success
            return finalPath;
        } else {
            // Move failed.
            // Case A: Another process created it concurrently -> Delete temp and use theirs.
            // Case B: Access denied / File locked? -> Still use theirs if exists.
            DeleteFileA(tempPath.c_str());

            // Re-check existence
            if (GetFileAttributesA(finalPath.c_str()) != INVALID_FILE_ATTRIBUTES) {
                return finalPath;
            }
        }
    }

    return "";
}

} // namespace embed
