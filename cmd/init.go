package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init [project-name]",
	Short: "Initialize a new xll-gen project",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		projectName := args[0]
		if err := runInit(projectName); err != nil {
			fmt.Printf("Error initializing project: %v\n", err)
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(initCmd)
}

func runInit(projectName string) error {
	fmt.Printf("Initializing project %s...\n", projectName)

	if _, err := os.Stat(projectName); !os.IsNotExist(err) {
		return fmt.Errorf("directory %s already exists", projectName)
	}

	if err := os.Mkdir(projectName, 0755); err != nil {
		return err
	}

	// 1. Create xll.yaml
	xllContent := `project:
  name: "` + projectName + `"
  version: "0.1.0"

gen:
  go:
    package: "generated"

server:
  timeout: "10s"
  workers: 100

# Function Definitions
# Supported types: int, float, string, bool
functions:
  # Simple integer addition
  - name: "Add"
    description: "Adds two integers"
    category: "Math"
    args:
      - name: "a"
        type: "int"
        description: "First number"
      - name: "b"
        type: "int"
        description: "Second number"
    return: "int"

  # Async function example
  - name: "GetPrice"
    description: "Fetches price for a ticker (Async simulation)"
    category: "Finance"
    args:
      - name: "ticker"
        type: "string"
        description: "Stock Ticker"
    return: "float"
    async: true

  # String manipulation
  - name: "Greet"
    description: "Returns a greeting message"
    category: "Text"
    args:
      - name: "name"
        type: "string"
        description: "Name to greet"
    return: "string"

  # Boolean logic
  - name: "IsEven"
    description: "Checks if a number is even"
    category: "Logic"
    args:
      - name: "val"
        type: "int"
        description: "Value to check"
    return: "bool"
`
	if err := os.WriteFile(filepath.Join(projectName, "xll.yaml"), []byte(xllContent), 0644); err != nil {
		return err
	}

	// 2. Create main.go
	mainContent := `package main

import (
	"context"
	"fmt"
	"` + projectName + `/generated"
	"time"
)

type MyService struct{}

func (s *MyService) Add(ctx context.Context, a int32, b int32) (int32, error) {
	return a + b, nil
}

func (s *MyService) GetPrice(ctx context.Context, ticker string) (float64, error) {
	// Simulate async work
	select {
	case <-time.After(100 * time.Millisecond):
		return 123.45, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (s *MyService) Greet(ctx context.Context, name string) (string, error) {
	return fmt.Sprintf("Hello, %s!", name), nil
}

func (s *MyService) IsEven(ctx context.Context, val int32) (bool, error) {
	return val%2 == 0, nil
}

func main() {
	// Connects to SHM and starts processing
	generated.Serve(&MyService{})
}
`
	if err := os.WriteFile(filepath.Join(projectName, "main.go"), []byte(mainContent), 0644); err != nil {
		return err
	}

	// 3. Create .gitignore
	gitIgnore := `build/
generated/
temp*/
`
	if err := os.WriteFile(filepath.Join(projectName, ".gitignore"), []byte(gitIgnore), 0644); err != nil {
		return err
	}

	// 4. Initialize go module
	cmd := exec.Command("go", "mod", "init", projectName)
	cmd.Dir = projectName
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to run go mod init: %w", err)
	}

	// 5. Create generated assets (C++ common files)
	// We put them in generated/cpp/include
	includeDir := filepath.Join(projectName, "generated", "cpp", "include")
	if err := os.MkdirAll(includeDir, 0755); err != nil {
		return err
	}

	for name, content := range assetsMap {
		if err := os.WriteFile(filepath.Join(includeDir, name), []byte(content), 0644); err != nil {
			return err
		}
	}

	fmt.Printf("Project %s initialized successfully!\n", projectName)
	fmt.Println("Next steps:")
	fmt.Printf("  cd %s\n", projectName)
	fmt.Println("  xll-gen generate  # (Run this to generate code)")
	fmt.Println("  xll-gen build     # (Run this to build the project)")

	return nil
}
