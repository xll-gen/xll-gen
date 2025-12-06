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

	// 5. Create generated assets (C++ common files)
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
