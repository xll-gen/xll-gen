package generator

import (
	"path/filepath"

	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/version"
)

// generateCppMain generates the main C++ file (xll_main.cpp)
func generateCppMain(cfg *config.Config, dir string, shouldAppendPid bool) error {
	data := struct {
		ProjectName     string
		Functions       []config.Function
		Events          []config.Event
		Server          config.ServerConfig
		Embed           config.BuildConfig
		ShouldAppendPid bool
		Version         string
		Logging         config.LoggingConfig
		Cache           config.CacheConfig
	}{
		ProjectName:     cfg.Project.Name,
		Functions:       cfg.Functions,
		Events:          cfg.Events,
		Server:          cfg.Server,
		Embed:           cfg.Build,
		ShouldAppendPid: shouldAppendPid,
		Version:         version.Version,
		Logging:         cfg.Logging,
		Cache:           cfg.Cache,
	}

	return executeTemplate("xll_main.cpp.tmpl", filepath.Join(dir, "xll_main.cpp"), data, GetCommonFuncMap())
}

// generateCMake generates the CMakeLists.txt file.
func generateCMake(cfg *config.Config, dir string) error {
	data := struct {
		ProjectName string
		Build       config.BuildConfig // Renamed from Embed to Build to match template usage
		Version     string
	}{
		ProjectName: cfg.Project.Name,
		Build:       cfg.Build,
		Version:     version.Version,
	}

	return executeTemplate("CMakeLists.txt.tmpl", filepath.Join(dir, "CMakeLists.txt"), data, nil)
}

// generateCppLaunch generates the C++ launch configuration
func generateCppLaunch(cfg *config.Config, dir string) error {
	// Not implemented yet - using static asset for now
	return nil
}
