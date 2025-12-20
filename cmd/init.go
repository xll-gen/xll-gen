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

var (
	force bool
	dev   bool
)

// initCmd represents the init command.
var initCmd = &cobra.Command{
	Use:   "init [project-name]",
	Short: "Initialize a new xll-gen project",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		projectName := args[0]
		if err := runInit(projectName, force, dev); err != nil {
			fmt.Printf("Error initializing project: %v\n", err)
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().BoolVarP(&force, "force", "f", false, "Force overwrite of existing directory")
	initCmd.Flags().BoolVar(&dev, "dev", false, "Use main branch for xll-gen dependency")
}

// runInit scaffolds a new project directory with the specified name.
func runInit(projectPath string, force, dev bool) error {
	projectName := filepath.Base(projectPath)
	printHeader(fmt.Sprintf("🚀 Initializing project %s...", projectName))

	if _, err := os.Stat(projectPath); !os.IsNotExist(err) {
		if force {
			if err := os.RemoveAll(projectPath); err != nil {
				return fmt.Errorf("failed to remove existing directory %s: %w", projectPath, err)
			}
		} else {
			return fmt.Errorf("directory %s already exists", projectPath)
		}
	}

	if err := os.Mkdir(projectPath, 0755); err != nil {
		return err
	}

	if err := generateFileFromTemplate("xll.yaml.tmpl", filepath.Join(projectPath, "xll.yaml"), struct {
		ProjectName string
		DevMode     bool
	}{projectName, dev}); err != nil {
		return err
	}
	printSuccess("Created", "xll.yaml")

	if err := generateFileFromTemplate("main.go.tmpl", filepath.Join(projectPath, "main.go"), struct{ ProjectName string }{projectName}); err != nil {
		return err
	}
	printSuccess("Created", "main.go")

	if !dev {
		if err := generateFileFromTemplate("gitignore.tmpl", filepath.Join(projectPath, ".gitignore"), nil); err != nil {
			return err
		}
		printSuccess("Created", ".gitignore")
	}

	vscodeDir := filepath.Join(projectPath, ".vscode")
	if err := os.MkdirAll(vscodeDir, 0755); err != nil {
		return err
	}
	if err := generateFileFromTemplate("launch.json.tmpl", filepath.Join(vscodeDir, "launch.json"), struct{ ProjectName string }{projectName}); err != nil {
		return err
	}
	printSuccess("Created", ".vscode/launch.json")

	cmd := exec.Command("go", "mod", "init", projectName)
	cmd.Dir = projectPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Println(string(output))
		return fmt.Errorf("failed to run go mod init: %w", err)
	}
	printSuccess("Initialized", "Go module")

	data, err := os.ReadFile(filepath.Join(projectPath, "xll.yaml"))
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
	opts := generator.Options{
		DevMode: dev,
	}
	if err := generator.Generate(&cfg, projectPath, projectName, opts); err != nil {
		return fmt.Errorf("failed to generate code: %w", err)
	}

	fmt.Printf("\n%s✨ Project %s initialized successfully!%s\n", colorGreen, projectName, colorReset)
	printHeader("Next steps:")
	fmt.Printf("  %scd %s%s\n", colorCyan, projectPath, colorReset)
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
