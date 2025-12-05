# VS Code 디버깅 가이드

`xll-gen`으로 생성된 프로젝트는 **Excel 프로세스(C++ XLL)**와 **Go 서버 프로세스**가 공유 메모리를 통해 통신하는 구조입니다. 따라서 디버깅을 위해서는 이 두 가지를 각각 VS Code에서 연결해주어야 합니다.

이 문서는 VS Code에서 `launch.json`을 구성하여 두 프로세스를 디버깅하는 방법을 설명합니다.

## 1. 사전 요구 사항 (Prerequisites)

VS Code에 다음 확장이 설치되어 있어야 합니다.

1.  **Go**: `golang.go` (Go 언어 디버깅용)
2.  **C/C++**: `ms-vscode.cpptools` (C++ 디버깅용)

## 2. launch.json 설정

`.vscode/launch.json` 파일을 생성하고 아래 내용을 복사하여 붙여넣으세요. 프로젝트 환경에 맞춰 일부 경로(특히 Excel 경로)를 수정해야 할 수 있습니다.

```json
{
    "version": "0.2.0",
    "configurations": [
        {
            "name": "1. Debug Go Server",
            "type": "go",
            "request": "launch",
            "mode": "auto",
            "program": "${workspaceFolder}/main.go",
            "env": {
                "GOOS": "windows",
                "GOARCH": "amd64"
            }
        },
        {
            "name": "2. Debug XLL (Excel) - MSVC",
            "type": "cppvsdbg",
            "request": "launch",
            "program": "C:\\Program Files\\Microsoft Office\\root\\Office16\\EXCEL.EXE",
            "args": ["${workspaceFolder}/build/Debug/YOUR_PROJECT_NAME.xll"],
            "stopAtEntry": false,
            "cwd": "${workspaceFolder}",
            "environment": [],
            "console": "externalTerminal",
            "description": "Visual Studio 컴파일러(MSVC)를 사용한 경우 선택하세요."
        },
        {
            "name": "2. Debug XLL (Excel) - MinGW/GDB",
            "type": "cppdbg",
            "request": "launch",
            "program": "C:\\Program Files\\Microsoft Office\\root\\Office16\\EXCEL.EXE",
            "args": ["${workspaceFolder}/build/YOUR_PROJECT_NAME.xll"],
            "stopAtEntry": false,
            "cwd": "${workspaceFolder}",
            "environment": [],
            "externalConsole": true,
            "MIMode": "gdb",
            "miDebuggerPath": "C:\\ProgramData\\chocolatey\\bin\\gdb.exe",
            "setupCommands": [
                {
                    "description": "Enable pretty-printing for gdb",
                    "text": "-enable-pretty-printing",
                    "ignoreFailures": true
                }
            ],
            "description": "MinGW(GCC)를 사용한 경우 선택하세요. gdb 경로는 설치 위치에 따라 수정이 필요합니다."
        }
    ]
}
```

### ⚠️ 주의사항

1.  **Excel 경로**: `program` 필드의 `EXCEL.EXE` 경로는 사용자 PC의 Office 설치 위치에 따라 다를 수 있습니다. 본인의 경로를 확인하여 수정하세요.
2.  **XLL 경로**: `args` 필드의 XLL 파일 경로와 이름(`YOUR_PROJECT_NAME.xll`)을 실제 빌드된 파일명으로 변경하세요.
    *   MSVC(CMake) 기본 출력: `build/Debug/프로젝트명.xll`
    *   MinGW 기본 출력: `build/프로젝트명.xll`
3.  **GDB 경로**: MinGW 사용 시 `miDebuggerPath`를 본인의 `gdb.exe` 위치로 수정해야 합니다.

## 3. 디버깅 순서 (Workflow)

`xll-gen` 아키텍처상 **Excel(XLL)이 먼저 실행되어 공유 메모리를 생성**해야 하고, 그 후 **Go 서버가 실행되어 접속**해야 합니다.

1.  **빌드**: 우선 프로젝트를 빌드합니다. (CMake 및 `go build` 사용)
    *   현재 `xll-gen` CLI는 `build` 명령을 제공하지 않으므로, C++ 부분은 CMake를 통해 수동 빌드해야 할 수 있습니다.
2.  **Excel 실행 (C++ 디버깅)**:
    *   VS Code의 '실행 및 디버그' 탭에서 **"2. Debug XLL (Excel)..."**을 선택하고 실행(F5)합니다.
    *   Excel이 실행되고 빈 통합 문서가 열립니다. (XLL이 로드된 상태)
3.  **Go 서버 실행 (Go 디버깅)**:
    *   Excel이 켜져 있는 상태에서, VS Code로 돌아와 **"1. Debug Go Server"**를 선택하고 실행(F5)합니다.
    *   Go 서버 터미널에 로그가 출력되며 정상적으로 연결되었음을 확인합니다.
4.  **테스트**:
    *   Excel 셀에 `=Add(1, 2)` 와 같은 함수를 입력하여 중단점(Breakpoint)이 잡히는지 확인합니다.

## 4. 문제 해결

*   **Go 서버가 바로 종료되는 경우**: Excel이 켜져 있지 않거나 XLL이 로드되지 않아 공유 메모리를 찾지 못한 경우입니다. Excel을 먼저 실행하세요.
*   **Excel이 중단점을 무시하는 경우**: PDB(디버그 심볼) 파일이 XLL과 같은 폴더에 있는지 확인하세요. 빌드 시 `Debug` 모드로 빌드했는지 확인해야 합니다.
