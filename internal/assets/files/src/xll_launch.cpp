#include "xll_launch.h"
#include "types/utility.h"
#include "xll_util.h"
#include "xll_log.h"
#include "xll_embed.h"
#include <vector>
#include <sstream>
#include <fstream>
#include <map>
#include <set>
#include <algorithm>
#include <cwctype>

namespace xll {

    static bool FileExists(const std::wstring& path) {
        DWORD dwAttrib = GetFileAttributesW(path.c_str());
        return (dwAttrib != INVALID_FILE_ATTRIBUTES && !(dwAttrib & FILE_ATTRIBUTE_DIRECTORY));
    }

    void ResolveServerPath(
        const std::wstring& xllDir,
        const std::wstring& extractedExe,
        const LaunchConfig& cfg,
        std::wstring& outCmd,
        std::wstring& outCwd
    ) {
        std::wstring defaultBinPath;
        if (!extractedExe.empty()) {
            defaultBinPath = extractedExe;
        } else {
             std::wstring sameDir = xllDir + L"\\" + cfg.projectName + L".exe";
             if (FileExists(sameDir)) {
                 defaultBinPath = sameDir;
             } else {
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

        std::wstring binDir = xllDir;
        size_t lastSlashBin = defaultBinPath.find_last_of(L"\\");
        if (lastSlashBin != std::wstring::npos) {
            binDir = defaultBinPath.substr(0, lastSlashBin);
        }

        std::wstring cwd = binDir;
        if (!cfg.cwd.empty()) {
            std::wstring wCwdCfg = StringToWString(cfg.cwd);
            ReplaceAll(wCwdCfg, L"${BIN_DIR}", binDir);
            ReplaceAll(wCwdCfg, L"${XLL_DIR}", xllDir);

            bool isAbs = (wCwdCfg.find(L":") != std::wstring::npos || (wCwdCfg.size() > 1 && wCwdCfg[0] == L'\\' && wCwdCfg[1] == L'\\'));
            if (isAbs) {
                cwd = wCwdCfg;
            } else {
                cwd = binDir + L"\\" + wCwdCfg;
            }
        }

        std::wstring exePath = defaultBinPath;
        if (!cfg.command.empty()) {
            std::string cfgCmd = cfg.command;
            std::wstring wCmd = StringToWString(cfgCmd);

            std::wstring varBin = L"${BIN}";
            if (wCmd.find(varBin) != std::wstring::npos) {
                ReplaceAll(wCmd, varBin, defaultBinPath);
                exePath = wCmd;
            } else {
                if (cfg.isSingleFile) {
                    std::string projExe = WideToUtf8(cfg.projectName) + ".exe";
                    if (cfgCmd.find(projExe) != std::string::npos) {
                    } else {
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

        std::wstring cmd;
        if (!exePath.empty() && exePath[0] == L'"') {
            cmd = exePath + L" -xll-shm=\"" + StringToWString(cfg.shmName) + L"\"";
        } else {
            cmd = L"\"" + exePath + L"\" -xll-shm=\"" + StringToWString(cfg.shmName) + L"\"";
        }

        outCmd = cmd;
        outCwd = cwd;
    }

    bool LaunchServer(const LaunchConfig& cfg, const std::wstring& xllDir, ProcessInfo& outInfo) {
        std::wstring extractedExe = L"";
        if (cfg.isSingleFile) {
            std::string tempDir = WideToUtf8(cfg.tempDir);
            if (tempDir.empty()) tempDir = "%TEMP%"; // Fallback
            std::string exe = embed::ExtractEmbeddedExe(tempDir, WideToUtf8(cfg.projectName));
            if (exe.empty()) {
                 LogInfo("No embedded executable found or extraction failed. Trying external...");
            } else {
                extractedExe = StringToWString(exe);
            }
        }

        std::wstring launchCmd, launchCwd;
        ResolveServerPath(xllDir, extractedExe, cfg, launchCmd, launchCwd);

        LogInfo("Launching Server: " + WideToUtf8(launchCmd));

        std::map<std::wstring, std::wstring> env;
        env[L"XLL_DIR"] = xllDir;
        env[L"XLL_SHM"] = StringToWString(cfg.shmName);

        outInfo.hShutdownEvent = CreateEvent(NULL, TRUE, FALSE, NULL);

        if (!LaunchProcess(launchCmd, launchCwd, outInfo, env)) {
             MessageBoxA(NULL, "Failed to launch server process. See native log.", "XLL Error", MB_OK | MB_ICONERROR);
             return false;
        }
        return true;
    }

    std::vector<wchar_t> CreateEnvBlock(const std::map<std::wstring, std::wstring>& env) {
        std::vector<wchar_t> block;
        std::set<std::wstring> seenKeys;

        for (const auto& kv : env) {
            std::wstring entry = kv.first + L"=" + kv.second;
            block.insert(block.end(), entry.begin(), entry.end());
            block.push_back(0);
            std::wstring keyUpper = kv.first;
            std::transform(keyUpper.begin(), keyUpper.end(), keyUpper.begin(), ::towupper);
            seenKeys.insert(keyUpper);
        }

        LPWCH currEnv = GetEnvironmentStringsW();
        if (currEnv) {
            LPWCH ptr = currEnv;
            while (*ptr) {
                size_t len = wcslen(ptr);
                std::wstring entry(ptr, len);
                size_t eqPos = entry.find(L'=');
                if (eqPos != std::wstring::npos) {
                    std::wstring key = entry.substr(0, eqPos);
                    if (!key.empty() && key[0] != L'=') {
                        std::wstring keyUpper = key;
                        std::transform(keyUpper.begin(), keyUpper.end(), keyUpper.begin(), ::towupper);
                        if (seenKeys.find(keyUpper) == seenKeys.end()) {
                            block.insert(block.end(), entry.begin(), entry.end());
                            block.push_back(0);
                        }
                    } else {
                         block.insert(block.end(), entry.begin(), entry.end());
                         block.push_back(0);
                    }
                }
                ptr += len + 1;
            }
            FreeEnvironmentStringsW(currEnv);
        }
        block.push_back(0);
        return block;
    }

    bool LaunchProcess(const std::wstring& cmd, const std::wstring& cwd, ProcessInfo& outInfo, const std::map<std::wstring, std::wstring>& extraEnv) {
        outInfo.hJob = CreateJobObject(NULL, NULL);
        if (outInfo.hJob) {
            JOBOBJECT_EXTENDED_LIMIT_INFORMATION jeli = { 0 };
            jeli.BasicLimitInformation.LimitFlags = JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE;
            SetInformationJobObject(outInfo.hJob, JobObjectExtendedLimitInformation, &jeli, sizeof(jeli));
        }

        // Create Pipe for Stdout/Stderr
        SECURITY_ATTRIBUTES saAttr;
        saAttr.nLength = sizeof(SECURITY_ATTRIBUTES);
        saAttr.bInheritHandle = TRUE;
        saAttr.lpSecurityDescriptor = NULL;

        HANDLE hChildStd_OUT_Rd = NULL;
        HANDLE hChildStd_OUT_Wr = NULL;

        if (!CreatePipe(&hChildStd_OUT_Rd, &hChildStd_OUT_Wr, &saAttr, 0)) {
            LogError("Stdout pipe creation failed");
            if (outInfo.hJob) CloseHandle(outInfo.hJob);
            return false;
        }

        // Ensure the read handle to the pipe for STDOUT is not inherited.
        if (!SetHandleInformation(hChildStd_OUT_Rd, HANDLE_FLAG_INHERIT, 0)) {
             LogError("Stdout SetHandleInformation failed");
             CloseHandle(hChildStd_OUT_Rd);
             CloseHandle(hChildStd_OUT_Wr);
             if (outInfo.hJob) CloseHandle(outInfo.hJob);
             return false;
        }

        outInfo.hStdOutRead = hChildStd_OUT_Rd;

        STARTUPINFOW si;
        ZeroMemory(&si, sizeof(si));
        si.cb = sizeof(si);
        si.dwFlags |= STARTF_USESTDHANDLES;
        si.hStdOutput = hChildStd_OUT_Wr;
        si.hStdError = hChildStd_OUT_Wr;
        si.hStdInput = NULL; // We don't need stdin for now

        PROCESS_INFORMATION pi;
        ZeroMemory(&pi, sizeof(pi));

        std::vector<wchar_t> cmdBuf(cmd.begin(), cmd.end());
        cmdBuf.push_back(0);

        void* envBlock = NULL;
        std::vector<wchar_t> envVec;
        if (!extraEnv.empty()) {
            envVec = CreateEnvBlock(extraEnv);
            envBlock = envVec.data();
        }

        DWORD flags = CREATE_UNICODE_ENVIRONMENT;

        if (CreateProcessW(NULL, cmdBuf.data(), NULL, NULL, TRUE, flags, envBlock, cwd.c_str(), &si, &pi)) {
            outInfo.hProcess = pi.hProcess;
            CloseHandle(pi.hThread);
            // Close the write end of the pipe in the parent process!
            CloseHandle(hChildStd_OUT_Wr);

            if (outInfo.hJob) {
                AssignProcessToJobObject(outInfo.hJob, outInfo.hProcess);
            }
            return true;
        } else {
            std::wstring msg = L"Failed to launch Go server.\nCommand: " + cmd;
            LogError(WideToUtf8(msg));
            MessageBoxW(NULL, msg.c_str(), L"Launch Error", MB_OK | MB_ICONERROR);

            CloseHandle(hChildStd_OUT_Wr);
            CloseHandle(hChildStd_OUT_Rd);
            if (outInfo.hJob) CloseHandle(outInfo.hJob);
            return false;
        }
    }

    bool LaunchProcess(const std::wstring& cmd, const std::wstring& cwd, ProcessInfo& outInfo) {
        std::map<std::wstring, std::wstring> emptyEnv;
        return LaunchProcess(cmd, cwd, outInfo, emptyEnv);
    }

    void MonitorProcess(const ProcessInfo& info) {
        HANDLE handles[2] = { info.hProcess, info.hShutdownEvent };
        DWORD res = WaitForMultipleObjects(2, handles, FALSE, INFINITE);

        if (res == WAIT_OBJECT_0) {
            // Process exited
            if (WaitForSingleObject(info.hShutdownEvent, 0) == WAIT_TIMEOUT) {
                // Not a clean shutdown (hShutdownEvent not set)
                DWORD exitCode = 0;
                GetExitCodeProcess(info.hProcess, &exitCode);

                std::wstringstream ss;
                ss << L"The Go server process has terminated unexpectedly (Exit Code: " << exitCode << L").\n";
                ss << L"The Add-in will no longer function correctly.\n\n";
                ss << L"Please check the native log file for details.";

                MessageBoxW(NULL, ss.str().c_str(), L"Server Crash", MB_OK | MB_ICONERROR);
            }
        }
    }

    void ForwardServerLogs(HANDLE hPipe) {
        const int BUFSIZE = 4096;
        char buffer[BUFSIZE];
        DWORD dwRead;
        std::string pending;

        while (ReadFile(hPipe, buffer, BUFSIZE - 1, &dwRead, NULL) && dwRead > 0) {
             buffer[dwRead] = '\0';
             std::string chunk(buffer, dwRead);

             // Append to pending
             pending += chunk;

             // Split by newline
             size_t pos = 0;
             while ((pos = pending.find('\n')) != std::string::npos) {
                 std::string line = pending.substr(0, pos);

                 // Remove \r if present
                 if (!line.empty() && line.back() == '\r') {
                     line.pop_back();
                 }

                 if (!line.empty()) {
                     LogInfo("[Server] " + line);
                 }

                 pending = pending.substr(pos + 1);
             }
        }

        // Log remaining
        if (!pending.empty()) {
             LogInfo("[Server] " + pending);
        }

        CloseHandle(hPipe);
    }
}
