#pragma once
#include <windows.h>
#include <string>

namespace xll {
    struct ProcessInfo {
        HANDLE hProcess;
        HANDLE hJob;
        HANDLE hShutdownEvent;
    };

    // Launches the server process with the given command line and working directory.
    // Initializes the Job Object and redirects stdout/stderr to logPath.
    // Returns true on success.
    bool LaunchProcess(const std::wstring& cmd, const std::wstring& cwd, const std::wstring& logPath, ProcessInfo& outInfo);

    // blocking function that monitors the child process.
    // It waits for the process to exit or for hShutdownEvent to be signaled.
    // If the process crashes, it reads the tail of the log file and shows a MessageBox.
    void MonitorProcess(const ProcessInfo& info, const std::wstring& logPath);
}
