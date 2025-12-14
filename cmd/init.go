package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"

	"github.com/spf13/cobra"
	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/internal/generator"
	"github.com/xll-gen/xll-gen/internal/templates"
	"gopkg.in/yaml.v3"
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
func runInit(projectName string, force bool) error {
	printHeader(fmt.Sprintf("🚀 Initializing project %s...", projectName))

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

	if err := generateFileFromTemplate("xll.yaml.tmpl", filepath.Join(projectName, "xll.yaml"), struct{ ProjectName string }{projectName}); err != nil {
		return err
	}
	printSuccess("Created", "xll.yaml")

	if err := generateFileFromTemplate("main.go.tmpl", filepath.Join(projectName, "main.go"), struct{ ProjectName string }{projectName}); err != nil {
		return err
	}
	printSuccess("Created", "main.go")

	if err := generateFileFromTemplate("gitignore.tmpl", filepath.Join(projectName, ".gitignore"), nil); err != nil {
		return err
	}
	printSuccess("Created", ".gitignore")

	vscodeDir := filepath.Join(projectName, ".vscode")
	if err := os.MkdirAll(vscodeDir, 0755); err != nil {
		return err
	}
	if err := generateFileFromTemplate("launch.json.tmpl", filepath.Join(vscodeDir, "launch.json"), struct{ ProjectName string }{projectName}); err != nil {
		return err
	}
	printSuccess("Created", ".vscode/launch.json")

	cmd := exec.Command("go", "mod", "init", projectName)
	cmd.Dir = projectName
	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Println(string(output))
		return fmt.Errorf("failed to run go mod init: %w", err)
	}
	printSuccess("Initialized", "Go module")

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

	// We use the project name as the module name since we just ran 'go mod init <projectName>'
	fmt.Println("") // Add spacing
	opts := generator.Options{}
	if err := generator.Generate(&cfg, projectName, opts); err != nil {
		return fmt.Errorf("failed to generate code: %w", err)
	}

	fmt.Printf("\n%s✨ Project %s initialized successfully!%s\n", colorGreen, projectName, colorReset)
	printHeader("Next steps:")
	fmt.Printf("  %scd %s%s\n", colorCyan, projectName, colorReset)
	fmt.Printf("  %sxll-gen build%s\n", colorCyan, colorReset)

	return nil
}

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
