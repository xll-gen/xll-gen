#pragma once
#include <windows.h>
#include <string>
#include <map>

namespace xll {
    struct ProcessInfo {
        HANDLE hProcess;
        HANDLE hJob;
        HANDLE hShutdownEvent;
    };

    struct LaunchConfig {
        std::wstring projectName;
        bool isSingleFile;
        std::wstring tempDir;      // Used only if logic needs to know temp dir (e.g. for logging)
        std::string cwd;           // from xll.yaml server.launch.cwd
        std::string command;       // from xll.yaml server.launch.command
        std::string shmName;       // Shared Memory Name
    };

    // Resolves the command to run and the working directory.
    // xllDir: The directory containing the .xll file.
    // extractedExe: Path to the extracted executable (if singlefile mode). Empty otherwise.
    // cfg: Configuration.
    // outCmd: Output command line (including arguments).
    // outCwd: Output working directory.
    // outLogPath: Output path for xll_launch.log
    void ResolveServerPath(
        const std::wstring& xllDir,
        const std::wstring& extractedExe,
        const LaunchConfig& cfg,
        std::wstring& outCmd,
        std::wstring& outCwd,
        std::wstring& outLogPath
    );

    // Launches the server process with the given command line and working directory.
    // Initializes the Job Object and redirects stdout/stderr to logPath.
    // Returns true on success.
    bool LaunchProcess(const std::wstring& cmd, const std::wstring& cwd, const std::wstring& logPath, ProcessInfo& outInfo);

    // Overload allowing extra environment variables
    bool LaunchProcess(const std::wstring& cmd, const std::wstring& cwd, const std::wstring& logPath, ProcessInfo& outInfo, const std::map<std::wstring, std::wstring>& extraEnv);

    // blocking function that monitors the child process.
    // It waits for the process to exit or for hShutdownEvent to be signaled.
    // If the process crashes, it reads the tail of the log file and shows a MessageBox.
    void MonitorProcess(const ProcessInfo& info, const std::wstring& logPath);
}
