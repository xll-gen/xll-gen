#include "include/xll_launch.h"
#include "include/xll_utility.h"
#include "include/xll_log.h"
#include <vector>
#include <sstream>
#include <fstream>

namespace xll {

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
