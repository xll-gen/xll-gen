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
    void ResolveServerPath(
        const std::wstring& xllDir,
        const std::wstring& extractedExe,
        const LaunchConfig& cfg,
        std::wstring& outCmd,
        std::wstring& outCwd,
        std::wstring& outLogPath
    );

    // High-level helper to launch the server
    bool LaunchServer(const LaunchConfig& cfg, const std::wstring& xllDir, ProcessInfo& outInfo, std::wstring& outLogPath);

    // Low-level launch
    bool LaunchProcess(const std::wstring& cmd, const std::wstring& cwd, const std::wstring& logPath, ProcessInfo& outInfo);
    bool LaunchProcess(const std::wstring& cmd, const std::wstring& cwd, const std::wstring& logPath, ProcessInfo& outInfo, const std::map<std::wstring, std::wstring>& extraEnv);

    void MonitorProcess(const ProcessInfo& info, const std::wstring& logPath);
}
