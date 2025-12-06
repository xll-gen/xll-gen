package cmd

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/template"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var simulateCmd = &cobra.Command{
	Use:   "simulate",
	Short: "Run a smoke test simulation (Mock Host)",
	Run: func(cmd *cobra.Command, args []string) {
		if err := runSimulate(); err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(simulateCmd)
}

func runSimulate() error {
	// 1. Check prerequisites
	if _, err := exec.LookPath("cmake"); err != nil {
		return fmt.Errorf("cmake not found. Please install CMake")
	}

	// 2. Run Generate
	fmt.Println("[1/6] Running generate...")
	if err := runGenerate(); err != nil {
		return err
	}

	// 3. Load Config
	data, err := os.ReadFile("xll.yaml")
	if err != nil {
		return fmt.Errorf("failed to read xll.yaml: %w", err)
	}
	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("failed to parse xll.yaml: %w", err)
	}

	// 4. Build Go Server
	fmt.Println("[2/6] Building Go server...")

	// Ensure dependencies
	if err := exec.Command("go", "mod", "tidy").Run(); err != nil {
		return fmt.Errorf("go mod tidy failed: %w", err)
	}

	serverBinName := config.Project.Name
	if runtime.GOOS == "windows" {
		serverBinName += ".exe"
	}
	buildDir := "build"
	if err := os.MkdirAll(buildDir, 0755); err != nil {
		return err
	}
	serverPath := filepath.Join(buildDir, serverBinName)

	buildCmd := exec.Command("go", "build", "-o", serverPath, "main.go")
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("failed to build Go server: %w", err)
	}

	// 5. Generate Simulation Host
	fmt.Println("[3/6] Generating Simulation Host...")
	simDir := "temp_simulation"
	if err := os.MkdirAll(simDir, 0755); err != nil {
		return err
	}
	if err := generateSimMain(config, simDir); err != nil {
		return err
	}
	if err := generateSimCMake(config, simDir); err != nil {
		return err
	}

	// 6. Build Simulation Host
	fmt.Println("[4/6] Building Simulation Host...")
	// cmake -S simulation -B simulation/build
	cmakeConfig := exec.Command("cmake", "-S", simDir, "-B", filepath.Join(simDir, "build"))
	// Quiet output unless error
	if out, err := cmakeConfig.CombinedOutput(); err != nil {
		fmt.Println(string(out))
		return fmt.Errorf("cmake config failed: %w", err)
	}

	// cmake --build simulation/build --config Release
	cmakeBuild := exec.Command("cmake", "--build", filepath.Join(simDir, "build"), "--config", "Release")
	if out, err := cmakeBuild.CombinedOutput(); err != nil {
		fmt.Println(string(out))
		return fmt.Errorf("cmake build failed: %w", err)
	}

	// 7. Run Simulation
	fmt.Println("[5/6] Starting Simulation...")

	// Locate mock_host
	mockBinName := "mock_host"
	if runtime.GOOS == "windows" {
		mockBinName += ".exe"
	}
	mockPath := filepath.Join(simDir, "build", mockBinName)
	if _, err := os.Stat(mockPath); os.IsNotExist(err) {
		mockPath = filepath.Join(simDir, "build", "Release", mockBinName)
	}

	// Start Mock Host
	hostCmd := exec.Command(mockPath)
	hostStdout, err := hostCmd.StdoutPipe()
	if err != nil {
		return err
	}
	hostCmd.Stderr = os.Stderr
	if err := hostCmd.Start(); err != nil {
		return fmt.Errorf("failed to start mock host: %w", err)
	}
	defer func() {
		if hostCmd.Process != nil {
			hostCmd.Process.Kill()
		}
	}()

	// Wait for "READY"
	scanner := bufio.NewScanner(hostStdout)
	ready := false
	for scanner.Scan() {
		line := scanner.Text()
		fmt.Println("[MockHost]", line)
		if strings.Contains(line, "READY") {
			ready = true
			break
		}
	}
	if !ready {
		return fmt.Errorf("mock host failed to start or did not signal READY")
	}

	// Start Go Server
	fmt.Println("[6/6] Starting Go Server...")
	serverCmd := exec.Command(serverPath, "-xll-shm="+config.Project.Name)
	serverCmd.Stdout = os.Stdout
	serverCmd.Stderr = os.Stderr
	if err := serverCmd.Start(); err != nil {
		return fmt.Errorf("failed to start server: %w", err)
	}
	defer func() {
		if serverCmd.Process != nil {
			serverCmd.Process.Kill()
		}
	}()

	// Read remaining output from Host
	// The host should run tests and exit
	exitCode := 0
	done := make(chan error)
	go func() {
		// Consume rest of stdout
		for scanner.Scan() {
			fmt.Println("[MockHost]", scanner.Text())
		}
		done <- hostCmd.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			// Check if it's an exit code error
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = 1
			}
		}
	case <-time.After(30 * time.Second):
		fmt.Println("Simulation timed out")
		exitCode = 1
		hostCmd.Process.Kill()
	}

	if exitCode == 0 {
		fmt.Println("Simulation PASSED")
	} else {
		fmt.Println("Simulation FAILED")
	}

	return nil
}

func generateSimMain(config Config, dir string) error {
	tmpl := `
#include <iostream>
#include <vector>
#include <thread>
#include <chrono>
#include "shm/DirectHost.h"
#include "schema_generated.h"

using namespace std;

int main() {
    shm::DirectHost host;
    string shmName = "{{.Project.Name}}";

    // Init SHM
    if (!host.Init(shmName.c_str(), 1024, 1024*1024, 16)) {
        cerr << "Failed to init SHM" << endl;
        return 1;
    }

    cout << "READY" << endl;

    // Wait for Guest to connect
    cout << "Waiting for guest..." << endl;
    auto start = chrono::steady_clock::now();
    bool connected = false;

    // We loop for a bit waiting for guest calls or just sleep?
    // Guest connects, but Host doesn't know until Guest sends a message?
    // No, standard SHM protocol usually doesn't have "Client Connected" event unless implemented.
    // However, we are sending requests to Guest.
    // We can just start sending. If Guest is not ready, it might miss it?
    // Guest "Connect" just opens the SHM.
    // Guest "Start" starts the loop.

    // We'll give the guest a second to start up
    this_thread::sleep_for(chrono::seconds(2));

    cout << "Sending requests..." << endl;
    int failures = 0;

{{range $i, $fn := .Functions}}
    {
        cout << "Testing {{.Name}}..." << endl;
        flatbuffers::FlatBufferBuilder builder(1024);

        // Construct Request with default values
        {{range .Args}}
        {{if eq .Type "string"}}
        auto {{.Name}}_off = builder.CreateString("test");
        {{else if eq .Type "string?"}}
        auto val_{{.Name}} = builder.CreateString("test");
        auto {{.Name}}_off = ipc::types::CreateStr(builder, val_{{.Name}});
        {{else if eq .Type "range"}}
        // Skip range for smoke test
        flatbuffers::Offset<ipc::types::Range> {{.Name}}_off(0);
        {{else if eq .Type "int?"}}
        auto {{.Name}}_off = ipc::types::CreateInt(builder, 1);
        {{else if eq .Type "float?"}}
        auto {{.Name}}_off = ipc::types::CreateNum(builder, 1.0);
        {{else if eq .Type "bool?"}}
        auto {{.Name}}_off = ipc::types::CreateBool(builder, true);
        {{end}}
        {{end}}

        ipc::{{.Name}}RequestBuilder reqBuilder(builder);
        {{range .Args}}
        {{if eq .Type "string"}}
        reqBuilder.add_{{.Name}}({{.Name}}_off);
        {{else if eq .Type "string?"}}
        reqBuilder.add_{{.Name}}({{.Name}}_off);
        {{else if eq .Type "int"}}
        reqBuilder.add_{{.Name}}(1);
        {{else if eq .Type "float"}}
        reqBuilder.add_{{.Name}}(1.0);
        {{else if eq .Type "bool"}}
        reqBuilder.add_{{.Name}}(true);
        {{else if eq .Type "range"}}
        if ({{.Name}}_off.o != 0) reqBuilder.add_{{.Name}}({{.Name}}_off);
        {{else if eq .Type "int?"}}
        reqBuilder.add_{{.Name}}({{.Name}}_off);
        {{else if eq .Type "float?"}}
        reqBuilder.add_{{.Name}}({{.Name}}_off);
        {{else if eq .Type "bool?"}}
        reqBuilder.add_{{.Name}}({{.Name}}_off);
        {{end}}
        {{end}}

        {{if .Async}}
        // Async requires handle. Host must provide it?
        // In real XLL, Excel provides it.
        // We simulate it by passing a dummy pointer (address of something).
        uint64_t dummyHandle = 12345ULL + {{$i}}; // Random unique
        reqBuilder.add_async_handle(dummyHandle);
        {{end}}

        auto req = reqBuilder.Finish();
        builder.Finish(req);

        // Send
        vector<uint8_t> respBuf;
        int size = host.Send(builder.GetBufferPointer(), builder.GetSize(), {{add 11 $i}}, respBuf);
        if (size < 0) {
            cerr << "  Send failed!" << endl;
            failures++;
        } else {
            {{if .Async}}
            cout << "  Async Request sent." << endl;
            auto start_wait = chrono::steady_clock::now();
            while(chrono::steady_clock::now() - start_wait < chrono::seconds(1)) {
                host.ProcessGuestCalls([](const uint8_t* req, int32_t size, uint8_t* resp, uint32_t msgId) -> int32_t {
                    return 0;
                });
                this_thread::sleep_for(chrono::milliseconds(10));
            }
            {{else}}
            auto resp = flatbuffers::GetRoot<ipc::{{.Name}}Response>(respBuf.data());
            if (resp->error() && resp->error()->size() > 0) {
                 cout << "  Got Error: " << resp->error()->str() << endl;
            } else {
                 cout << "  Success" << endl;
            }
            {{end}}
        }
    }
{{end}}

    if (failures > 0) return 1;
    return 0;
}
`
	funcMap := template.FuncMap{
		"add": func(a, b int) int { return a + b },
	}

	t, err := template.New("sim_main").Funcs(funcMap).Parse(tmpl)
	if err != nil {
		return err
	}

	f, err := os.Create(filepath.Join(dir, "main.cpp"))
	if err != nil {
		return err
	}
	defer f.Close()

	return t.Execute(f, config)
}

func generateSimCMake(config Config, dir string) error {
	tmpl := `cmake_minimum_required(VERSION 3.14)
project(mock_host LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)

include(FetchContent)

# Flatbuffers
FetchContent_Declare(
  flatbuffers
  GIT_REPOSITORY https://github.com/google/flatbuffers.git
  GIT_TAG v25.9.23
)
FetchContent_MakeAvailable(flatbuffers)

# SHM
FetchContent_Declare(
  shm
  GIT_REPOSITORY https://github.com/xll-gen/shm.git
  GIT_TAG main
)
FetchContent_MakeAvailable(shm)

if(NOT TARGET shm)
  add_library(shm INTERFACE)
  target_include_directories(shm INTERFACE ${shm_SOURCE_DIR}/include)
endif()

add_executable(mock_host main.cpp)

# Include generated headers from main project
target_include_directories(mock_host PRIVATE ../generated/cpp)

target_link_libraries(mock_host PRIVATE
  shm
  flatbuffers
)
`
	t, err := template.New("sim_cmake").Parse(tmpl)
	if err != nil {
		return err
	}

	f, err := os.Create(filepath.Join(dir, "CMakeLists.txt"))
	if err != nil {
		return err
	}
	defer f.Close()

	return t.Execute(f, config)
}
