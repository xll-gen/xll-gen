#ifdef _WIN32
#include <windows.h>
#else
#include <unistd.h>
#include <sys/stat.h>
#include <sys/wait.h>
#include "embedded_data.h" // Generated on Linux
#endif

#include <stdio.h>
#include <vector>
#include <string>
#include <zstd.h>
#include <stdlib.h>

// Resource ID for Windows
#define IDR_GO_BINARY 101

void ErrorExit(const char* msg) {
#ifdef _WIN32
    fprintf(stderr, "Error: %s (LastError: %lu)\n", msg, GetLastError());
    ExitProcess(1);
#else
    perror(msg);
    exit(1);
#endif
}

int main() {
    printf("Host: Starting...\n");

    const void* pData = NULL;
    size_t size = 0;

#ifdef _WIN32
    // --- Windows Resource Loading ---
    HMODULE hModule = GetModuleHandle(NULL);
    HRSRC hRes = FindResource(hModule, MAKEINTRESOURCE(IDR_GO_BINARY), RT_RCDATA);
    if (!hRes) ErrorExit("FindResource failed");

    HGLOBAL hLoad = LoadResource(hModule, hRes);
    if (!hLoad) ErrorExit("LoadResource failed");

    pData = LockResource(hLoad);
    if (!pData) ErrorExit("LockResource failed");
    size = SizeofResource(hModule, hRes);
    printf("Host: Found compressed resource of size %lu bytes (Windows RC).\n", (unsigned long)size);

#else
    // --- Linux Array Access ---
    pData = guest_zst;
    size = guest_zst_len;
    printf("Host: Found compressed data of size %zu bytes (Linux Header).\n", size);
#endif

    // --- Common Decompression ---
    unsigned long long const decompressedSize = ZSTD_getFrameContentSize(pData, size);
    if (decompressedSize == ZSTD_CONTENTSIZE_ERROR) {
        fprintf(stderr, "Error: Not a valid Zstd file\n");
        exit(1);
    }
    if (decompressedSize == ZSTD_CONTENTSIZE_UNKNOWN) {
        fprintf(stderr, "Error: Original size unknown\n");
        exit(1);
    }

    std::vector<char> outBuffer(decompressedSize);
    size_t const dSize = ZSTD_decompress(outBuffer.data(), decompressedSize, pData, size);
    if (ZSTD_isError(dSize)) {
        fprintf(stderr, "Zstd Decompression Error: %s\n", ZSTD_getErrorName(dSize));
        return 1;
    }
    printf("Host: Decompressed to %llu bytes.\n", (unsigned long long)dSize);

    // --- Write to Temp File ---
    std::string exePath;

#ifdef _WIN32
    char tempPath[MAX_PATH];
    if (GetTempPathA(MAX_PATH, tempPath) == 0) ErrorExit("GetTempPath failed");
    exePath = std::string(tempPath) + "embedded_guest.exe";
#else
    exePath = "./embedded_guest"; // Use current dir or /tmp
#endif

    printf("Host: Extracting to %s\n", exePath.c_str());

    FILE* f = fopen(exePath.c_str(), "wb");
    if (!f) ErrorExit("fopen failed");
    fwrite(outBuffer.data(), 1, dSize, f);
    fclose(f);

#ifndef _WIN32
    // chmod +x on Linux
    if (chmod(exePath.c_str(), S_IRUSR | S_IWUSR | S_IXUSR) != 0) {
        ErrorExit("chmod failed");
    }
#endif

    // --- Execute ---
    printf("Host: Executing guest...\n");

#ifdef _WIN32
    STARTUPINFOA si;
    PROCESS_INFORMATION pi;
    ZeroMemory(&si, sizeof(si));
    si.cb = sizeof(si);
    ZeroMemory(&pi, sizeof(pi));

    if (!CreateProcessA(exePath.c_str(), "embedded_guest.exe arg1", NULL, NULL, FALSE, 0, NULL, NULL, &si, &pi)) {
        ErrorExit("CreateProcess failed");
    }
    WaitForSingleObject(pi.hProcess, INFINITE);
    CloseHandle(pi.hProcess);
    CloseHandle(pi.hThread);
#else
    // Linux execution
    std::string cmd = exePath + " arg1";
    int ret = system(cmd.c_str());
    if (ret == -1) {
        ErrorExit("system() failed");
    }
#endif

    printf("Host: Guest finished.\n");

    // Cleanup
    remove(exePath.c_str());

    return 0;
}
