package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/internal/generator"
	"github.com/xll-gen/xll-gen/internal/templates"
)

var force bool

// initCmd represents the init command.
var initCmd = &cobra.Command{
	Use:   "init [project-name]",
	Short: "Initialize a new xll-gen project",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		projectName := args[0]
		if err := runInit(projectName, force); err != nil {
			fmt.Printf("Error initializing project: %v\n", err)
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().BoolVarP(&force, "force", "f", false, "Force overwrite of existing directory")
}

// runInit scaffolds a new project directory with the specified name.
// It creates configuration files, the main Go entry point, and initial assets.
//
// Parameters:
//   - projectName: The name of the project (and directory) to create.
//   - force: If true, existing directory will be removed.
//
// Returns:
//   - error: An error if the directory already exists (and force is false) or file creation fails.
func runInit(projectName string, force bool) error {
	fmt.Printf("Initializing project %s...\n", projectName)

	if _, err := os.Stat(projectName); !os.IsNotExist(err) {
		if force {
			if err := os.RemoveAll(projectName); err != nil {
				return fmt.Errorf("failed to remove existing directory %s: %w", projectName, err)
			}
		} else {
			return fmt.Errorf("directory %s already exists", projectName)
		}
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

	// 4. Create .vscode/launch.json
	vscodeDir := filepath.Join(projectName, ".vscode")
	if err := os.MkdirAll(vscodeDir, 0755); err != nil {
		return err
	}
	if err := generateFileFromTemplate("launch.json.tmpl", filepath.Join(vscodeDir, "launch.json"), struct{ ProjectName string }{projectName}); err != nil {
		return err
	}

	// 5. Initialize go module
	cmd := exec.Command("go", "mod", "init", projectName)
	cmd.Dir = projectName
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to run go mod init: %w", err)
	}

	// 6. Generate code and run go mod tidy
	// We need to change directory to the project folder for generation and tidy
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := os.Chdir(projectName); err != nil {
		return err
	}
	defer func() {
		// Restore original working directory
		_ = os.Chdir(cwd)
	}()

	// Read and parse xll.yaml
	data, err := os.ReadFile("xll.yaml")
	if err != nil {
		return fmt.Errorf("failed to read xll.yaml: %w", err)
	}

	var cfg config.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("failed to parse xll.yaml: %w", err)
	}

	config.ApplyDefaults(&cfg)

	if err := config.Validate(&cfg); err != nil {
		return err
	}

	// Run generator
	// We use the project name as the module name since we just ran 'go mod init <projectName>'
	opts := generator.Options{}
	if err := generator.Generate(&cfg, projectName, opts); err != nil {
		return fmt.Errorf("failed to generate code: %w", err)
	}

	// Run go mod tidy
	fmt.Println("Running 'go mod tidy'...")
	cmdTidy := exec.Command("go", "mod", "tidy")
	cmdTidy.Stdout = os.Stdout
	cmdTidy.Stderr = os.Stderr
	if err := cmdTidy.Run(); err != nil {
		return fmt.Errorf("failed to run go mod tidy: %w", err)
	}

	fmt.Printf("Project %s initialized successfully!\n", projectName)
	fmt.Println("Next steps:")
	fmt.Printf("  cd %s\n", projectName)
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
