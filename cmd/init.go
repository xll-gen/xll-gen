package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"

	"github.com/spf13/cobra"
	"xll-gen/internal/assets"
	"xll-gen/internal/templates"
)

// initCmd represents the init command.
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

// runInit scaffolds a new project directory with the specified name.
// It creates configuration files, the main Go entry point, and initial assets.
//
// Parameters:
//   - projectName: The name of the project (and directory) to create.
//
// Returns:
//   - error: An error if the directory already exists or file creation fails.
func runInit(projectName string) error {
	fmt.Printf("Initializing project %s...\n", projectName)

	if _, err := os.Stat(projectName); !os.IsNotExist(err) {
		return fmt.Errorf("directory %s already exists", projectName)
	}

	if err := os.Mkdir(projectName, 0755); err != nil {
		return err
	}

	// 1. Create xll.yaml
	if err := generateFileFromTemplate("xll.yaml.tmpl", filepath.Join(projectName, "xll.yaml"), struct{ ProjectName string }{projectName}); err != nil {
		return err
	}

	// 2. Create main.go
	if err := generateFileFromTemplate("main.go.tmpl", filepath.Join(projectName, "main.go"), struct{ ProjectName string }{projectName}); err != nil {
		return err
	}

	// 3. Create .gitignore
	if err := generateFileFromTemplate("gitignore.tmpl", filepath.Join(projectName, ".gitignore"), nil); err != nil {
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

	// 5. Run go mod tidy
	// This ensures go.sum is created and the module is in a consistent state,
	// even if dependencies (like shm) aren't imported yet.
	cmdTidy := exec.Command("go", "mod", "tidy")
	cmdTidy.Dir = projectName
	cmdTidy.Stdout = os.Stdout
	cmdTidy.Stderr = os.Stderr
	if err := cmdTidy.Run(); err != nil {
		return fmt.Errorf("failed to run go mod tidy: %w", err)
	}

	// 6. Create generated assets (C++ common files)
	// We put them in generated/cpp/include
	includeDir := filepath.Join(projectName, "generated", "cpp", "include")
	if err := os.MkdirAll(includeDir, 0755); err != nil {
		return err
	}

	for name, content := range assets.AssetsMap {
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

// generateFileFromTemplate creates a file at destPath using the specified template and data.
//
// Parameters:
//   - tmplName: The name of the template file to use.
//   - destPath: The path where the generated file should be written.
//   - data: The data object to pass to the template.
//
// Returns:
//   - error: An error if the template cannot be read or executed.
func generateFileFromTemplate(tmplName, destPath string, data interface{}) error {
	content, err := templates.Get(tmplName)
	if err != nil {
		return err
	}
	t, err := template.New(tmplName).Parse(content)
	if err != nil {
		return err
	}
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()
	return t.Execute(f, data)
}
