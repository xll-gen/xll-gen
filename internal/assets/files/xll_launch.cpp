#include "include/xll_launch.h"
#include "include/xll_utility.h"
#include "include/xll_log.h"
#include <vector>
#include <sstream>
#include <fstream>

namespace xll {

    // Helper to replace all occurrences of a substring
    static void ReplaceAll(std::wstring& str, const std::wstring& from, const std::wstring& to) {
        if(from.empty()) return;
        size_t start_pos = 0;
        while((start_pos = str.find(from, start_pos)) != std::wstring::npos) {
            str.replace(start_pos, from.length(), to);
            start_pos += to.length();
        }
    }

    static bool FileExists(const std::wstring& path) {
        DWORD dwAttrib = GetFileAttributesW(path.c_str());
        return (dwAttrib != INVALID_FILE_ATTRIBUTES && !(dwAttrib & FILE_ATTRIBUTE_DIRECTORY));
    }

    void ResolveServerPath(
        const std::wstring& xllDir,
        const std::wstring& extractedExe,
        const LaunchConfig& cfg,
        std::wstring& outCmd,
        std::wstring& outCwd,
        std::wstring& outLogPath
    ) {
        std::wstring cwd = xllDir;

        // Apply Config Cwd
        if (!cfg.cwd.empty() && cfg.cwd != ".") {
            std::wstring wCwd = StringToWString(cfg.cwd);
            bool isAbs = (wCwd.find(L":") != std::wstring::npos || (wCwd.size() > 1 && wCwd[0] == L'\\' && wCwd[1] == L'\\'));
            if (isAbs) {
                cwd = wCwd;
            } else {
                cwd = xllDir + L"\\" + wCwd;
            }
        }

        std::wstring defaultBinPath;

        // 1. Determine Default Binary Path
        if (!extractedExe.empty()) {
            defaultBinPath = extractedExe;
            // In singlefile mode, run from the temp dir so logs appear there
            size_t lastSlash = defaultBinPath.find_last_of(L"\\");
            if (lastSlash != std::wstring::npos) {
                cwd = defaultBinPath.substr(0, lastSlash);
            }
        } else {
             // Fallback: Check standard locations
             std::wstring sameDir = xllDir + L"\\" + cfg.projectName + L".exe";
             if (FileExists(sameDir)) {
                 defaultBinPath = sameDir;
             } else {
                 // Check parent directory
                 size_t lastSlash = xllDir.find_last_of(L"\\");
                 if (lastSlash != std::wstring::npos) {
                     std::wstring parentDir = xllDir.substr(0, lastSlash);
                     std::wstring parentExe = parentDir + L"\\" + cfg.projectName + L".exe";
                     if (FileExists(parentExe)) {
                         defaultBinPath = parentExe;
                     } else {
                         defaultBinPath = sameDir;
                     }
                 } else {
                      defaultBinPath = sameDir;
                 }
             }
        }

        // 2. Resolve Configured Command
        std::wstring exePath = defaultBinPath;
        if (!cfg.command.empty()) {
            std::string cfgCmd = cfg.command;
            std::wstring wCmd = StringToWString(cfgCmd);

            // Check for ${BIN} variable
            std::wstring varBin = L"${BIN}";
            if (wCmd.find(varBin) != std::wstring::npos) {
                ReplaceAll(wCmd, varBin, defaultBinPath);
                exePath = wCmd;
            } else {
                if (cfg.isSingleFile) {
                    // In singlefile mode, if the command explicitly points to the project executable (default config),
                    // we should ignore it and use the extracted binary path instead.
                    std::string projExe = WideToUtf8(cfg.projectName) + ".exe";
                    if (cfgCmd.find(projExe) != std::string::npos) {
                        // Ignore, keep exePath as defaultBinPath (extracted)
                    } else {
                        // Custom command handling
                        bool isCmdAbs = (wCmd.find(L":") != std::wstring::npos || (wCmd.size() > 1 && wCmd[0] == L'\\' && wCmd[1] == L'\\'));
                        if (isCmdAbs) {
                            exePath = wCmd;
                        } else {
                             if (cfgCmd.find("./") == 0 || cfgCmd.find(".\\") == 0) {
                                 exePath = cwd + L"\\" + wCmd.substr(2);
                            } else {
                                 exePath = cwd + L"\\" + wCmd;
                            }
                        }
                    }
                } else {
                    // Standard Mode
                     bool isCmdAbs = (wCmd.find(L":") != std::wstring::npos || (wCmd.size() > 1 && wCmd[0] == L'\\' && wCmd[1] == L'\\'));
                     if (isCmdAbs) {
                         exePath = wCmd;
                     } else {
                          if (cfgCmd.find("./") == 0 || cfgCmd.find(".\\") == 0) {
                              exePath = cwd + L"\\" + wCmd.substr(2);
                         } else {
                              exePath = cwd + L"\\" + wCmd;
                         }
                     }
                }
            }
        }

        // Append SHM Flag
        std::wstring cmd;
        if (!exePath.empty() && exePath[0] == L'"') {
            cmd = exePath + L" -xll-shm=" + StringToWString(cfg.shmName);
        } else {
            cmd = L"\"" + exePath + L"\" -xll-shm=" + StringToWString(cfg.shmName);
        }

        outCmd = cmd;
        outCwd = cwd;
        outLogPath = cwd + L"\\xll_launch.log";
    }

    bool LaunchProcess(const std::wstring& cmd, const std::wstring& cwd, const std::wstring& logPath, ProcessInfo& outInfo) {
        // Create Job Object
        outInfo.hJob = CreateJobObject(NULL, NULL);
        if (outInfo.hJob) {
            JOBOBJECT_EXTENDED_LIMIT_INFORMATION jeli = { 0 };
            jeli.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE;
            SetInformationJobObject(outInfo.hJob, JobObjectExtendedLimitInformation, &jeli, sizeof(jeli));
        }

        SECURITY_ATTRIBUTES sa;
        sa.nLength = sizeof(SECURITY_ATTRIBUTES);
        sa.bInheritHandle = TRUE;
        sa.lpSecurityDescriptor = NULL;

        HANDLE hLog = CreateFileW(logPath.c_str(), FILE_APPEND_DATA, FILE_SHARE_READ, &sa, OPEN_ALWAYS, FILE_ATTRIBUTE_NORMAL, NULL);
        if (hLog == INVALID_HANDLE_VALUE) {
            LogError("Failed to open log file for launch: " + WideToUtf8(logPath));
            if (outInfo.hJob) CloseHandle(outInfo.hJob);
            return false;
        }

        STARTUPINFOW si;
        ZeroMemory(&si, sizeof(si));
        si.cb = sizeof(si);
        si.dwFlags |= STARTF_USESTDHANDLES;
        si.hStdOutput = hLog;
        si.hStdError = hLog;
        si.hStdInput = NULL;

        PROCESS_INFORMATION pi;
        ZeroMemory(&pi, sizeof(pi));

        std::vector<wchar_t> cmdBuf(cmd.begin(), cmd.end());
        cmdBuf.push_back(0);

        if (CreateProcessW(NULL, cmdBuf.data(), NULL, NULL, TRUE, 0, NULL, cwd.c_str(), &si, &pi)) {
            outInfo.hProcess = pi.hProcess;
            CloseHandle(pi.hThread);
            if (outInfo.hJob) {
                AssignProcessToJobObject(outInfo.hJob, outInfo.hProcess);
            }
            CloseHandle(hLog);
            return true;
        } else {
            std::wstring msg = L"Failed to launch Go server.\nCommand: " + cmd;
            LogError(WideToUtf8(msg));
            MessageBoxW(NULL, msg.c_str(), L"Launch Error", MB_OK | MB_ICONERROR);
            if (outInfo.hJob) CloseHandle(outInfo.hJob);
            CloseHandle(hLog);
            return false;
        }
    }

    void MonitorProcess(const ProcessInfo& info, const std::wstring& logPath) {
        HANDLE handles[2] = { info.hProcess, info.hShutdownEvent };
        DWORD res = WaitForMultipleObjects(2, handles, FALSE, INFINITE);

        if (res == WAIT_OBJECT_0) {
            // Process exited. Check if shutdown was requested concurrently.
            if (WaitForSingleObject(info.hShutdownEvent, 0) == WAIT_TIMEOUT) {
                // It was a crash (or unexpected exit)
                DWORD exitCode = 0;
                GetExitCodeProcess(info.hProcess, &exitCode);

                std::wstringstream ss;
                ss << L"The Go server process has terminated unexpectedly (Exit Code: " << exitCode << L").\n";
                ss << L"The Add-in will no longer function correctly.\n\n";
                ss << L"Last log entries:\n";

                HANDLE hRead = CreateFileW(logPath.c_str(), GENERIC_READ, FILE_SHARE_READ | FILE_SHARE_WRITE, NULL, OPEN_EXISTING, FILE_ATTRIBUTE_NORMAL, NULL);
                if (hRead != INVALID_HANDLE_VALUE) {
                    LARGE_INTEGER size;
                    if (GetFileSizeEx(hRead, &size)) {
                        long long length = size.QuadPart;
                        long long start = 0;
                        if (length > 1024) start = length - 1024;

                        LARGE_INTEGER move;
                        move.QuadPart = start;
                        SetFilePointerEx(hRead, move, NULL, FILE_BEGIN);

                        std::vector<char> buffer((size_t)(length - start));
                        DWORD bytesRead = 0;
                        if (ReadFile(hRead, buffer.data(), (DWORD)buffer.size(), &bytesRead, NULL)) {
                            std::string s(buffer.data(), bytesRead);
                            ss << StringToWString(s);
                        }
                    }
                    CloseHandle(hRead);
                } else {
                    ss << L"(Unable to read log file)";
                }

                MessageBoxW(NULL, ss.str().c_str(), L"Server Crash", MB_OK | MB_ICONERROR);
            }
        }
    }
}
