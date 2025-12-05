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

functions:
  - name: "Add"
    description: "Adds two integers"
    args:
      - name: "a"
        type: "int"
        description: "First number"
      - name: "b"
        type: "int"
        description: "Second number"
    return: "int"

  - name: "GetPrice"
    description: "Fetches price for a ticker"
    args:
      - name: "ticker"
        type: "string"
    return: "float"
    async: true
`
	if err := os.WriteFile(filepath.Join(projectName, "xll.yaml"), []byte(xllContent), 0644); err != nil {
		return err
	}

	// 2. Create main.go
	mainContent := `package main

import (
	"` + projectName + `/generated"
)

type MyService struct{}

func (s *MyService) Add(a int32, b int32) (int32, error) {
	return a + b, nil
}

func (s *MyService) GetPrice(ticker string) (float64, error) {
	return 100.50, nil // Mock
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
