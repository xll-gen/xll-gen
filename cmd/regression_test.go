package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
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

  # Any & Range
  - name: "CheckAny"
    args: [{name: "val", type: "any"}]
    return: "string"
  - name: "CheckRange"
    args: [{name: "val", type: "range"}]
    return: "string"

  # Timeout
  - name: "TimeoutFunc"
    args: [{name: "val", type: "int"}]
    return: "int"
    timeout: "100ms"
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
	"fmt"
	"smoke_proj/generated"
	"smoke_proj/generated/ipc/types"
    "time"

	flatbuffers "github.com/google/flatbuffers/go"
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

func (s *Service) TimeoutFunc(ctx context.Context, val int32) (int32, error) {
    select {
    case <-time.After(500 * time.Millisecond):
        return val, nil
    case <-ctx.Done():
        // Return fallback value on timeout
        return -1, nil
    }
}

func (s *Service) CheckAny(ctx context.Context, val *types.Any) (string, error) {
    if val == nil { return "Nil", nil }
    var tbl flatbuffers.Table
    if !val.Val(&tbl) { return "Unknown", nil }

    switch val.ValType() {
    case types.AnyValueInt:
        var t types.Int
        t.Init(tbl.Bytes, tbl.Pos)
        return fmt.Sprintf("Int:%d", t.Val()), nil
    case types.AnyValueNum:
        var t types.Num
        t.Init(tbl.Bytes, tbl.Pos)
        return fmt.Sprintf("Num:%.1f", t.Val()), nil
    case types.AnyValueStr:
        var t types.Str
        t.Init(tbl.Bytes, tbl.Pos)
        return fmt.Sprintf("Str:%s", string(t.Val())), nil
    case types.AnyValueBool:
        var t types.Bool
        t.Init(tbl.Bytes, tbl.Pos)
        return fmt.Sprintf("Bool:%v", t.Val()), nil
    case types.AnyValueErr:
        var t types.Err
        t.Init(tbl.Bytes, tbl.Pos)
        return fmt.Sprintf("Err:%d", t.Val()), nil
    case types.AnyValueNil:
        return "Nil", nil
    case types.AnyValueNumArray:
        var t types.NumArray
        t.Init(tbl.Bytes, tbl.Pos)
        return fmt.Sprintf("NumArray:%dx%d", t.Rows(), t.Cols()), nil
    case types.AnyValueArray:
        var t types.Array
        t.Init(tbl.Bytes, tbl.Pos)
        return fmt.Sprintf("Array:%dx%d", t.Rows(), t.Cols()), nil
    }
    return "Unknown", nil
}

func (s *Service) CheckRange(ctx context.Context, val *types.Range) (string, error) {
    if val == nil { return "Nil", nil }
    sheet := string(val.SheetName())
    if val.RefsLength() > 0 {
        var r types.Rect
        if val.Refs(&r, 0) {
             return fmt.Sprintf("Range:%s!%d:%d:%d:%d", sheet, r.RowFirst(), r.RowLast(), r.ColFirst(), r.ColLast()), nil
        }
    }
    return "RangeEmpty", nil
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

    // 6. EchoFloatOpt (ID 16)
    // Case: Value
    {
        builder.Reset();
        auto valOff = ipc::types::CreateNum(builder, 3.14);
        ipc::EchoFloatOptRequestBuilder req(builder);
        req.add_val(valOff);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), 16, respBuf) < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoFloatOptResponse>(respBuf.data());
        if (!resp->result()) { cerr << "Expected value" << endl; return 1; }
        if (std::abs(resp->result()->val() - 3.14) > 0.001) { cerr << "FloatOpt mismatch" << endl; return 1; }
    }
    // Case: Null
    {
        builder.Reset();
        ipc::EchoFloatOptRequestBuilder req(builder);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), 16, respBuf) < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoFloatOptResponse>(respBuf.data());
        if (resp->result()) { cerr << "Expected null" << endl; return 1; }
    }

    // 7. EchoStringOpt (ID 17)
    // Case: Value
    {
        builder.Reset();
        auto sOff = builder.CreateString("opt");
        auto valOff = ipc::types::CreateStr(builder, sOff);
        ipc::EchoStringOptRequestBuilder req(builder);
        req.add_val(valOff);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), 17, respBuf) < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoStringOptResponse>(respBuf.data());
        if (!resp->result()) { cerr << "Expected value" << endl; return 1; }
        ASSERT_STREQ("opt", resp->result()->val()->str(), "EchoStringOpt Val");
    }
    // Case: Null
    {
        builder.Reset();
        ipc::EchoStringOptRequestBuilder req(builder);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), 17, respBuf) < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoStringOptResponse>(respBuf.data());
        if (resp->result()) { cerr << "Expected null" << endl; return 1; }
    }

    // 8. EchoBoolOpt (ID 18)
    // Case: Value
    {
        builder.Reset();
        auto valOff = ipc::types::CreateBool(builder, true);
        ipc::EchoBoolOptRequestBuilder req(builder);
        req.add_val(valOff);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), 18, respBuf) < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoBoolOptResponse>(respBuf.data());
        if (!resp->result()) { cerr << "Expected value" << endl; return 1; }
        ASSERT_EQ(true, resp->result()->val(), "EchoBoolOpt Val");
    }
    // Case: Null
    {
        builder.Reset();
        ipc::EchoBoolOptRequestBuilder req(builder);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        if(host.Send(builder.GetBufferPointer(), builder.GetSize(), 18, respBuf) < 0) return 1;
        auto resp = flatbuffers::GetRoot<ipc::EchoBoolOptResponse>(respBuf.data());
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

    // 10. CheckAny (ID 20)
    // Int
    {
        builder.Reset();
        auto val = ipc::types::CreateInt(builder, 10);
        auto any = ipc::types::CreateAny(builder, ipc::types::AnyValue_Int, val.Union());
        ipc::CheckAnyRequestBuilder req(builder);
        req.add_val(any);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        host.Send(builder.GetBufferPointer(), builder.GetSize(), 20, respBuf);
        auto resp = flatbuffers::GetRoot<ipc::CheckAnyResponse>(respBuf.data());
        ASSERT_STREQ("Int:10", resp->result()->str(), "CheckAny Int");
    }
    // Str
    {
        builder.Reset();
        auto s = builder.CreateString("hello");
        auto val = ipc::types::CreateStr(builder, s);
        auto any = ipc::types::CreateAny(builder, ipc::types::AnyValue_Str, val.Union());
        ipc::CheckAnyRequestBuilder req(builder);
        req.add_val(any);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        host.Send(builder.GetBufferPointer(), builder.GetSize(), 20, respBuf);
        auto resp = flatbuffers::GetRoot<ipc::CheckAnyResponse>(respBuf.data());
        ASSERT_STREQ("Str:hello", resp->result()->str(), "CheckAny Str");
    }
    // Num
    {
        builder.Reset();
        auto val = ipc::types::CreateNum(builder, 1.5);
        auto any = ipc::types::CreateAny(builder, ipc::types::AnyValue_Num, val.Union());
        ipc::CheckAnyRequestBuilder req(builder);
        req.add_val(any);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        host.Send(builder.GetBufferPointer(), builder.GetSize(), 20, respBuf);
        auto resp = flatbuffers::GetRoot<ipc::CheckAnyResponse>(respBuf.data());
        ASSERT_STREQ("Num:1.5", resp->result()->str(), "CheckAny Num");
    }

    // NumArray
    {
        builder.Reset();
        std::vector<double> data = {1.1, 2.2};
        auto dataOff = builder.CreateVector(data);
        auto arr = ipc::types::CreateNumArray(builder, 1, 2, dataOff);
        auto any = ipc::types::CreateAny(builder, ipc::types::AnyValue_NumArray, arr.Union());
        ipc::CheckAnyRequestBuilder req(builder);
        req.add_val(any);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        host.Send(builder.GetBufferPointer(), builder.GetSize(), 20, respBuf);
        auto resp = flatbuffers::GetRoot<ipc::CheckAnyResponse>(respBuf.data());
        ASSERT_STREQ("NumArray:1x2", resp->result()->str(), "CheckAny NumArray");
    }

    // Array
    {
        builder.Reset();
        auto val1 = ipc::types::CreateInt(builder, 1);
        auto s1 = ipc::types::CreateScalar(builder, ipc::types::ScalarValue_Int, val1.Union());
        auto val2 = ipc::types::CreateBool(builder, true);
        auto s2 = ipc::types::CreateScalar(builder, ipc::types::ScalarValue_Bool, val2.Union());

        std::vector<flatbuffers::Offset<ipc::types::Scalar>> data = {s1, s2};
        auto dataOff = builder.CreateVector(data);
        auto arr = ipc::types::CreateArray(builder, 1, 2, dataOff);
        auto any = ipc::types::CreateAny(builder, ipc::types::AnyValue_Array, arr.Union());

        ipc::CheckAnyRequestBuilder req(builder);
        req.add_val(any);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        host.Send(builder.GetBufferPointer(), builder.GetSize(), 20, respBuf);
        auto resp = flatbuffers::GetRoot<ipc::CheckAnyResponse>(respBuf.data());
        ASSERT_STREQ("Array:1x2", resp->result()->str(), "CheckAny Array");
    }

    // 11. CheckRange (ID 21)
    {
        builder.Reset();
        auto sOff = builder.CreateString("Sheet1");
        std::vector<ipc::types::Rect> refs = { {1,1,1,1} };
        auto refsOff = builder.CreateVectorOfStructs(refs);
        auto rangeVal = ipc::types::CreateRange(builder, sOff, refsOff);
        ipc::CheckRangeRequestBuilder req(builder);
        req.add_val(rangeVal);
        builder.Finish(req.Finish());
        vector<uint8_t> respBuf;
        host.Send(builder.GetBufferPointer(), builder.GetSize(), 21, respBuf);
        auto resp = flatbuffers::GetRoot<ipc::CheckRangeResponse>(respBuf.data());
        ASSERT_STREQ("Range:Sheet1!1:1:1:1", resp->result()->str(), "CheckRange");
    }

    // 12. TimeoutFunc (ID 22)
    {
        builder.Reset();
        ipc::TimeoutFuncRequestBuilder req(builder);
        req.add_val(10);
        builder.Finish(req.Finish());

        vector<uint8_t> respBuf;
        host.Send(builder.GetBufferPointer(), builder.GetSize(), 22, respBuf);
        auto resp = flatbuffers::GetRoot<ipc::TimeoutFuncResponse>(respBuf.data());

        // Timeout now returns -1 instead of error
        ASSERT_EQ(-1, resp->result(), "TimeoutFunc");
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

    // Give Mock Host time to initialize SHM
    time.Sleep(2 * time.Second)

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
