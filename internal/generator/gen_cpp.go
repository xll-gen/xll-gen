package generator

import (
	"path/filepath"

	"xll-gen/internal/config"
	"xll-gen/version"
)

// generateCppMain generates the C++ entry point (xll_main.cpp) for the XLL.
// It populates the template with function registrations, event handlers, and IPC logic.
//
// Parameters:
//   - cfg: The project configuration.
//   - dir: The directory where the file should be generated.
//   - shouldAppendPid: Whether to append the process ID to the shared memory name.
//
// Returns:
//   - error: An error if generation fails.
func generateCppMain(cfg *config.Config, dir string, shouldAppendPid bool) error {
	data := struct {
		ProjectName     string
		Functions       []config.Function
		Events          []config.Event
		Server          config.ServerConfig
		ShouldAppendPid bool
		Version         string
	}{
		ProjectName:     cfg.Project.Name,
		Functions:       cfg.Functions,
		Events:          cfg.Events,
		Server:          cfg.Server,
		ShouldAppendPid: shouldAppendPid,
		Version:         version.Version,
	}

	return executeTemplate("xll_main.cpp.tmpl", filepath.Join(dir, "xll_main.cpp"), data, GetCommonFuncMap())
}

// generateCMake generates the CMakeLists.txt build file.
// It configures the C++ build system for the XLL, including dependencies.
//
// Parameters:
//   - cfg: The project configuration.
//   - dir: The directory where the file should be generated.
//
// Returns:
//   - error: An error if generation fails.
func generateCMake(cfg *config.Config, dir string) error {
	data := struct {
		ProjectName string
		Version     string
	}{
		ProjectName: cfg.Project.Name,
		Version:     version.Version,
	}

	return executeTemplate("CMakeLists.txt.tmpl", filepath.Join(dir, "CMakeLists.txt"), data, nil)
}
