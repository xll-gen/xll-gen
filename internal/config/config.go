package config

import (
	"fmt"
	"strings"
)

// Config represents the top-level configuration structure parsed from xll.yaml.
// It defines the project metadata, generation settings, server configuration,
// and the list of Excel functions and events.
type Config struct {
	// Project contains metadata about the project (name, version).
	Project   ProjectConfig `yaml:"project"`
	// Gen contains settings for code generation.
	Gen       GenConfig     `yaml:"gen"`
	// Build contains build configuration including embedding settings.
	Build     BuildConfig   `yaml:"build"`
	// Logging contains logging configuration.
	Logging   LoggingConfig `yaml:"logging"`
	// Server contains configuration for the Go server process.
	Server    ServerConfig  `yaml:"server"`
	// Functions is a list of Excel functions to register.
	Functions []Function    `yaml:"functions"`
	// Events is a list of Excel events to handle.
	Events    []Event       `yaml:"events"`
}

// LoggingConfig configures logging behavior.
type LoggingConfig struct {
	// Level is the log level (debug, info, warn, error).
	Level string `yaml:"level"`
	// Path is the log file path.
	Path string `yaml:"path"`
}

// Event defines a subscription to a specific Excel event.
type Event struct {
	// Type is the type of event (e.g., "CalculationEnded").
	Type        string `yaml:"type"`
	// Name is the name of the handler function to generate.
	Name        string `yaml:"name"`
	// Description describes the purpose of the event handler.
	Description string `yaml:"description"`
}

// BuildConfig contains build settings.
type BuildConfig struct {
	// Singlefile configures the embedding strategy.
	// Options: "xll" (embed Go server in XLL), "exe" (reserved/unimplemented), or empty (no embedding).
	Singlefile string `yaml:"singlefile"`
	// TempDir is the directory where embedded binaries are extracted (supports env vars).
	TempDir string `yaml:"temp_dir"`
}

// ServerConfig configures the runtime behavior of the Go server.
type ServerConfig struct {
	// Timeout is the default timeout for synchronous requests (e.g., "5s").
	Timeout         string        `yaml:"timeout"`
	// AsyncAckTimeout is the timeout for acknowledging async requests.
	AsyncAckTimeout string        `yaml:"async_ack_timeout"`
	// Workers is the size of the worker pool for processing requests.
	Workers         int           `yaml:"workers"`
	// Launch configures the auto-launch behavior of the server.
	Launch          *LaunchConfig `yaml:"launch"`
}

// LaunchConfig configures how the Go server is launched by the XLL.
type LaunchConfig struct {
	// Enabled determines if the XLL should automatically spawn the server.
	Enabled *bool  `yaml:"enabled"`
	// Command is the command to execute (defaults to the project executable).
	Command string `yaml:"command"`
	// Cwd is the working directory for the server process.
	Cwd     string `yaml:"cwd"`
}

// ProjectConfig contains basic project metadata.
type ProjectConfig struct {
	// Name is the name of the project.
	Name    string `yaml:"name"`
	// Version is the version of the project.
	Version string `yaml:"version"`
}

// GenConfig controls the code generation process.
type GenConfig struct {
	// Go contains Go-specific generation settings.
	Go               GoConfig `yaml:"go"`
	// DisablePidSuffix, if true, prevents appending the PID to the shared memory name.
	DisablePidSuffix bool     `yaml:"disable_pid_suffix"`
}

// GoConfig contains Go-specific code generation settings.
type GoConfig struct {
	// Package is the package name for the generated Go code.
	Package string `yaml:"package"`
}

// Function represents a single Excel function definition.
type Function struct {
	// Name is the name of the function as exposed to Excel.
	Name        string `yaml:"name"`
	// Description is the help text displayed in the Excel function wizard.
	Description string `yaml:"description"`
	// Args is the list of arguments accepted by the function.
	Args        []Arg  `yaml:"args"`
	// Return is the return type of the function (e.g., "int", "string", "any").
	Return      string `yaml:"return"`
	// Volatile marks the function as volatile (recalculated on every sheet change).
	Volatile    bool   `yaml:"volatile"`
	// Async marks the function as asynchronous.
	Async       bool   `yaml:"async"`
	// Category is the category under which the function appears in Excel.
	Category    string `yaml:"category"`
	// Shortcut is the keyboard shortcut for the function (e.g., "Ctrl+Shift+A").
	Shortcut    string `yaml:"shortcut"`
	// HelpTopic is the URL or path to the help topic.
	HelpTopic   string `yaml:"help_topic"`
	// Timeout is the execution timeout for this specific function.
	Timeout     string `yaml:"timeout"`
	// Caller indicates if the function requires information about the calling cell.
	Caller      bool   `yaml:"caller"`
}

// Arg represents a single argument of an Excel function.
type Arg struct {
	// Name is the name of the argument.
	Name        string `yaml:"name"`
	// Type is the data type of the argument (e.g., "int", "string", "range").
	Type        string `yaml:"type"`
	// Description is the help text for the argument.
	Description string `yaml:"description"`
}

// validArgTypes is the set of allowed argument types in xll.yaml.
var validArgTypes = map[string]bool{
	"int":     true,
	"float":   true,
	"string":  true,
	"bool":    true,
	"range":   true,
	"grid":    true,
	"numgrid": true,
	"any":     true,
}

// validReturnTypes is the set of allowed return types in xll.yaml.
var validReturnTypes = map[string]bool{
	"int":     true,
	"float":   true,
	"string":  true,
	"bool":    true,
	"range":   true,
	"grid":    true,
	"numgrid": true,
	"any":     true,
}

// Validate checks the configuration for errors, such as duplicate event types
// or unsupported argument types.
//
// Parameters:
//   - config: The Config object to validate.
//
// Returns:
//   - error: An error if the configuration is invalid, or nil otherwise.
func Validate(config *Config) error {
	if config.Build.Singlefile != "" {
		switch config.Build.Singlefile {
		case "xll":
			// ok
		case "exe":
			return fmt.Errorf("singlefile mode 'exe' is not supported yet")
		default:
			return fmt.Errorf("invalid singlefile mode: %s (allowed: xll)", config.Build.Singlefile)
		}
	}

	seenEvents := make(map[string]bool)
	for _, evt := range config.Events {
		if seenEvents[evt.Type] {
			return fmt.Errorf("duplicate event type: %s", evt.Type)
		}
		seenEvents[evt.Type] = true
	}

	for _, fn := range config.Functions {
		if !validReturnTypes[fn.Return] {
			return fmt.Errorf("function '%s': return type '%s' is not supported (allowed: %s)", fn.Name, fn.Return, allowedTypesList(validReturnTypes))
		}
		for _, arg := range fn.Args {
			if !validArgTypes[arg.Type] {
				// Special error message for nullable scalar types
				if strings.HasSuffix(arg.Type, "?") {
					return fmt.Errorf("function '%s' argument '%s': type '%s' is not supported (optional scalars are not supported by Excel API; use 'any' or 'scalar' instead)", fn.Name, arg.Name, arg.Type)
				}
				return fmt.Errorf("function '%s' argument '%s': type '%s' is not supported (allowed: %s)", fn.Name, arg.Name, arg.Type, allowedTypesList(validArgTypes))
			}
		}
	}

	if config.Logging.Level != "" {
		switch strings.ToLower(config.Logging.Level) {
		case "debug", "info", "warn", "error":
			// ok
		default:
			return fmt.Errorf("invalid logging level: %s (allowed: debug, info, warn, error)", config.Logging.Level)
		}
	}

	return nil
}

func allowedTypesList(m map[string]bool) string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	return strings.Join(keys, ", ")
}

// ApplyDefaults sets default values for configuration fields that are missing.
// For example, it enables server auto-launch if not explicitly disabled.
//
// Parameters:
//   - config: The Config object to modify.
func ApplyDefaults(config *Config) {
	if config.Build.Singlefile == "" {
		config.Build.Singlefile = "xll"
	}
	if config.Build.TempDir == "" {
		config.Build.TempDir = "${TEMP}"
	}

	if config.Server.Launch != nil {
		if config.Server.Launch.Enabled == nil {
			t := true
			config.Server.Launch.Enabled = &t
		}
	}

	if config.Logging.Level == "" {
		config.Logging.Level = "info"
	}
}
