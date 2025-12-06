package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRegression(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping regression test in short mode")
	}

	// 1. Setup Temp Dir
	tempDir, err := os.MkdirTemp("", "xll-regression-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	// 2. Init Project
	projectName := "smoke_proj"

	// Switch WD to tempDir to run init
	origWd, _ := os.Getwd()
	if err := os.Chdir(tempDir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWd)

	if err := runInit(projectName); err != nil {
		t.Fatalf("runInit failed: %v", err)
	}

	if err := os.Chdir(projectName); err != nil {
		t.Fatal(err)
	}

	// 3. Write Comprehensive xll.yaml
	xllYaml := `project:
  name: "smoke_proj"
  version: "0.1.0"

gen:
  go:
    package: "generated"

server:
  workers: 10
  timeout: "5s"

functions:
  - name: "EchoInt"
    args: [{name: "val", type: "int"}]
    return: "int"
  - name: "EchoFloat"
    args: [{name: "val", type: "float"}]
    return: "float"
  - name: "EchoString"
    args: [{name: "val", type: "string"}]
    return: "string"
  - name: "EchoBool"
    args: [{name: "val", type: "bool"}]
    return: "bool"

  # Nullables
  - name: "EchoIntOpt"
    args: [{name: "val", type: "int?"}]
    return: "int?"
  - name: "EchoFloatOpt"
    args: [{name: "val", type: "float?"}]
    return: "float?"
  - name: "EchoStringOpt"
    args: [{name: "val", type: "string?"}]
    return: "string?"
  - name: "EchoBoolOpt"
    args: [{name: "val", type: "bool?"}]
    return: "bool?"

  # Async
  - name: "AsyncEchoInt"
    args: [{name: "val", type: "int"}]
    return: "int"
    async: true
`
	if err := os.WriteFile("xll.yaml", []byte(xllYaml), 0644); err != nil {
		t.Fatal(err)
	}

	// 4. Run Generate
	if err := runGenerate(); err != nil {
		t.Fatalf("runGenerate failed: %v", err)
	}

	// 5. Write main.go (Server)
	mainGo := `package main

import (
	"context"
	"smoke_proj/generated"
	"smoke_proj/generated/ipc/types"
    "time"
)

// Force usage
var _ = types.Bool{}

type Service struct{}

func (s *Service) EchoInt(ctx context.Context, val int32) (int32, error) { return val, nil }
func (s *Service) EchoFloat(ctx context.Context, val float64) (float64, error) { return val, nil }
func (s *Service) EchoString(ctx context.Context, val string) (string, error) { return val, nil }
func (s *Service) EchoBool(ctx context.Context, val bool) (bool, error) { return val, nil }

func (s *Service) EchoIntOpt(ctx context.Context, val *int32) (*int32, error) { return val, nil }
func (s *Service) EchoFloatOpt(ctx context.Context, val *float64) (*float64, error) { return val, nil }
func (s *Service) EchoStringOpt(ctx context.Context, val *string) (*string, error) { return val, nil }
func (s *Service) EchoBoolOpt(ctx context.Context, val *bool) (*bool, error) { return val, nil }

func (s *Service) AsyncEchoInt(ctx context.Context, val int32) (int32, error) {
    time.Sleep(10 * time.Millisecond)
    return val, nil
}

func main() {
    generated.Serve(&Service{})
}
`
	if err := os.WriteFile("main.go", []byte(mainGo), 0644); err != nil {
		t.Fatal(err)
	}

	// 6. Go Mod Tidy
	cmd := exec.Command("go", "mod", "tidy")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy failed: %v\nOutput: %s", err, out)
	}

	// 7. Build Go Server
	serverBin := "smoke_proj"
	if runtime.GOOS == "windows" { serverBin += ".exe" }
	if err := os.MkdirAll("build", 0755); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("go", "build", "-o", filepath.Join("build", serverBin), "main.go")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\nOutput: %s", err, out)
	}

	// 8. Generate Simulation C++ Host (Manual)
	simDir := "temp_simulation"
	if err := os.MkdirAll(simDir, 0755); err != nil {
		t.Fatal(err)
	}

	cmakeContent := `cmake_minimum_required(VERSION 3.14)
project(mock_host LANGUAGES CXX)
set(CMAKE_CXX_STANDARD 17)
include(FetchContent)
FetchContent_Declare(flatbuffers GIT_REPOSITORY https://github.com/google/flatbuffers.git GIT_TAG v25.9.23)
FetchContent_MakeAvailable(flatbuffers)
FetchContent_Declare(shm GIT_REPOSITORY https://github.com/xll-gen/shm.git GIT_TAG main)
FetchContent_MakeAvailable(shm)
if(NOT TARGET shm)
  add_library(shm INTERFACE)
  target_include_directories(shm INTERFACE ${shm_SOURCE_DIR}/include)
endif()
add_executable(mock_host main.cpp)
target_include_directories(mock_host PRIVATE ../generated/cpp)
target_link_libraries(mock_host PRIVATE shm flatbuffers)
`
	if err := os.WriteFile(filepath.Join(simDir, "CMakeLists.txt"), []byte(cmakeContent), 0644); err != nil {
		t.Fatal(err)
	}

	cppContent := `
#include <iostream>
#include <vector>
#include <thread>
#include <chrono>
#include <cmath>
#include "shm/DirectHost.h"
#include "schema_generated.h"

using namespace std;

#define ASSERT_EQ(a, b, msg) { \
    if ((a) != (b)) { \
        cerr << "FAIL: " << msg << " Expected " << (a) << " got " << (b) << endl; \
        return 1; \
    } \
}
#define ASSERT_STREQ(a, b, msg) { \
    if (string(a) != string(b)) { \
        cerr << "FAIL: " << msg << " Expected '" << (a) << "' got '" << (b) << "'" << endl; \
        return 1; \
    } \
}

int main() {
    shm::DirectHost host;
    if (!host.Init("smoke_proj", 1024, 1024*1024, 16)) {
        cerr << "Failed to init SHM" << endl;
        return 1;
    }
    cout << "READY" << endl;
    this_thread::sleep_for(chrono::seconds(2)); // Wait for guest

    flatbuffers::FlatBufferBuilder builder(1024);

    // 1. EchoInt (ID 11)
    vector<int32_t> intCases = {0, 1, -1, 2147483647, (int32_t)-2147483648LL};
    for (auto val : intCases) {
        builder.Reset();
        ipc::EchoIntRequestBuilder req(builder);
        req.add_val(val);
        builder.Finish(req.Finish());

        vector<uint8_t> respBuf;
        int sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), 11, respBuf);
        if (sz < 0) { cerr << "Send failed for EchoInt " << val << endl; return 1; }

        auto resp = flatbuffers::GetRoot<ipc::EchoIntResponse>(respBuf.data());
        if (resp->error() && resp->error()->size() > 0) { cerr << "Error: " << resp->error()->str() << endl; return 1; }
        ASSERT_EQ(val, resp->result(), "EchoInt");
    }

    // 2. EchoFloat (ID 12)
    vector<double> floatCases = {0.0, 1.5, -999.99};
    for (auto val : floatCases) {
        builder.Reset();
        ipc::EchoFloatRequestBuilder req(builder);
        req.add_val(val);
        builder.Finish(req.Finish());

        vector<uint8_t> respBuf;
        int sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), 12, respBuf);
        if (sz < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoFloatResponse>(respBuf.data());
        if (std::abs(val - resp->result()) > 0.0001) { cerr << "Float mismatch" << endl; return 1; }
    }

    // 3. EchoString (ID 13)
    vector<string> strCases = {"test", "", "Hello World"};
    for (auto val : strCases) {
        builder.Reset();
        auto off = builder.CreateString(val);
        ipc::EchoStringRequestBuilder req(builder);
        req.add_val(off);
        builder.Finish(req.Finish());

        vector<uint8_t> respBuf;
        int sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), 13, respBuf);
        if (sz < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoStringResponse>(respBuf.data());
        ASSERT_STREQ(val, resp->result()->str(), "EchoString");
    }

    // 4. EchoBool (ID 14)
    vector<bool> boolCases = {true, false};
    for (auto val : boolCases) {
        builder.Reset();
        ipc::EchoBoolRequestBuilder req(builder);
        req.add_val(val);
        builder.Finish(req.Finish());

        vector<uint8_t> respBuf;
        int sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), 14, respBuf);
        if (sz < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoBoolResponse>(respBuf.data());
        ASSERT_EQ(val, resp->result(), "EchoBool");
    }

    // 5. EchoIntOpt (ID 15)
    // Case: Value
    {
        builder.Reset();
        auto valOff = ipc::types::CreateInt(builder, 123);
        ipc::EchoIntOptRequestBuilder req(builder);
        req.add_val(valOff);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), 15, respBuf) < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoIntOptResponse>(respBuf.data());
        if (!resp->result()) { cerr << "Expected value" << endl; return 1; }
        ASSERT_EQ(123, resp->result()->val(), "EchoIntOpt Val");
    }
    // Case: Null
    {
        builder.Reset();
        ipc::EchoIntOptRequestBuilder req(builder);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), 15, respBuf) < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoIntOptResponse>(respBuf.data());
        if (resp->result()) { cerr << "Expected null" << endl; return 1; }
    }

    // 9. AsyncEchoInt (ID 19)
    {
        builder.Reset();
        ipc::AsyncEchoIntRequestBuilder req(builder);
        req.add_val(42);
        req.add_async_handle(999);
        builder.Finish(req.Finish());

        vector<uint8_t> respBuf;
        int sz = host.Send(builder.GetBufferPointer(), builder.GetSize(), 19, respBuf);
        if (sz < 0) return 1;

        // Async returns immediately with void

        // Wait for callback
        bool gotCallback = false;
        auto start = chrono::steady_clock::now();
        while(chrono::steady_clock::now() - start < chrono::seconds(2)) {
            host.ProcessGuestCalls([&](const uint8_t* req, int32_t size, uint8_t* resp, uint32_t msgId) -> int32_t {
                // Check MsgID = 19
                if (msgId == 19) {
                     auto response = flatbuffers::GetRoot<ipc::AsyncEchoIntResponse>(req);
                     if (response->async_handle() == 999 && response->result() == 42) {
                         gotCallback = true;
                     }
                }
                return 0;
            });
            if (gotCallback) break;
            this_thread::sleep_for(chrono::milliseconds(10));
        }
        if (!gotCallback) { cerr << "Async callback missing" << endl; return 1; }
    }

    cout << "PASSED" << endl;
    return 0;
}
`
	if err := os.WriteFile(filepath.Join(simDir, "main.cpp"), []byte(cppContent), 0644); err != nil {
		t.Fatal(err)
	}

	// 9. Build Simulation
	cmd = exec.Command("cmake", "-S", simDir, "-B", filepath.Join(simDir, "build"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cmake config failed: %s", out)
	}

	cmd = exec.Command("cmake", "--build", filepath.Join(simDir, "build"), "--config", "Release")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cmake build failed: %s", out)
	}

	// 10. Run
	mockBin := filepath.Join(simDir, "build", "mock_host")
	if runtime.GOOS == "windows" {
		if _, err := os.Stat(mockBin + ".exe"); os.IsNotExist(err) {
			 mockBin = filepath.Join(simDir, "build", "Release", "mock_host.exe")
		} else {
			 mockBin += ".exe"
		}
	} else {
        // Linux/Mac
        if _, err := os.Stat(mockBin); os.IsNotExist(err) {
             // Maybe it is in ./mock_host if cmake didn't use Release folder?
             // Standard cmake puts it in build/mock_host
        }
    }

	mockCmd := exec.Command(mockBin)
	mockCmd.Stdout = os.Stdout
	mockCmd.Stderr = os.Stderr
	if err := mockCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if mockCmd.Process != nil { mockCmd.Process.Kill() }
	}()

	serverCmd := exec.Command(filepath.Join("build", serverBin), "-xll-shm=smoke_proj")
	serverCmd.Stdout = os.Stdout
	serverCmd.Stderr = os.Stderr
	if err := serverCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		 if serverCmd.Process != nil { serverCmd.Process.Kill() }
	}()

	if err := mockCmd.Wait(); err != nil {
		t.Fatalf("Mock host failed: %v", err)
	}
}
