package generator

import (
	"path/filepath"

	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/version"
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
		Build           config.BuildConfig
		ShouldAppendPid bool
		Version         string
		Logging         config.LoggingConfig
	}{
		ProjectName:     cfg.Project.Name,
		Functions:       cfg.Functions,
		Events:          cfg.Events,
		Server:          cfg.Server,
		Build:           cfg.Build,
		ShouldAppendPid: shouldAppendPid,
		Version:         version.Version,
		Logging:         cfg.Logging,
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
		Build       config.BuildConfig
	}{
		ProjectName: cfg.Project.Name,
		Version:     version.Version,
		Build:       cfg.Build,
	}

	return executeTemplate("CMakeLists.txt.tmpl", filepath.Join(dir, "CMakeLists.txt"), data, GetCommonFuncMap())
}
