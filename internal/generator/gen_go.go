package generator

import (
	"path/filepath"

	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/version"
)

// generateInterface generates the Go interface definition (interface.go).
// This interface defines the contract that the user's Go code must implement.
//
// Parameters:
//   - cfg: The project configuration.
//   - dir: The directory where the file should be generated.
//   - modName: The Go module name of the project.
//
// Returns:
//   - error: An error if generation fails.
func generateInterface(cfg *config.Config, dir string, modName string) error {
	pkg := cfg.Gen.Go.Package
	if pkg == "" {
		pkg = "generated"
	}

	data := struct {
		Package   string
		ModName   string
		Functions []config.Function
		Events    []config.Event
		Version   string
	}{
		Package:   pkg,
		ModName:   modName,
		Functions: cfg.Functions,
		Events:    cfg.Events,
		Version:   version.Version,
	}

	return executeTemplate("interface.go.tmpl", filepath.Join(dir, "interface.go"), data, GetCommonFuncMap())
}

// generateServer generates the Go server implementation (server.go).
// It includes the main loop, IPC handling, and dispatching to the user's handler.
//
// Parameters:
//   - cfg: The project configuration.
//   - dir: The directory where the file should be generated.
//   - modName: The Go module name of the project.
//
// Returns:
//   - error: An error if generation fails.
func generateServer(cfg *config.Config, dir string, modName string) error {
	pkg := cfg.Gen.Go.Package
	if pkg == "" {
		pkg = "generated"
	}

	data := struct {
		Package       string
		ModName       string
		ProjectName   string
		Functions     []config.Function
		Events        []config.Event
		ServerTimeout string
		ServerWorkers int
		Version       string
		Logging       config.LoggingConfig
	}{
		Package:       pkg,
		ModName:       modName,
		ProjectName:   cfg.Project.Name,
		Functions:     cfg.Functions,
		Events:        cfg.Events,
		ServerTimeout: cfg.Server.Timeout,
		ServerWorkers: cfg.Server.Workers,
		Version:       version.Version,
		Logging:       cfg.Logging,
	}

	return executeTemplate("server.go.tmpl", filepath.Join(dir, "server.go"), data, GetCommonFuncMap())
}
