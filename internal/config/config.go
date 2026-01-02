package config

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Config represents the top-level structure of the xll.yaml file.
// It maps directly to the YAML configuration provided by the user.
type Config struct {
	// Project contains project metadata.
	Project   ProjectConfig `yaml:"project"`
	// Build contains build-specific configuration.
	Build     BuildConfig   `yaml:"build"`
	// Logging contains logging configuration.
	Logging   LoggingConfig `yaml:"logging"`
	// Cache contains global caching configuration.
	Cache     CacheConfig   `yaml:"cache"`
	// Server contains configuration for the Go server process.
	Server    ServerConfig  `yaml:"server"`
	// Functions is a list of Excel functions to register.
	Functions []Function    `yaml:"functions"`
	// Gen contains code generation configuration.
	Gen       GenConfig     `yaml:"gen"`
	// Events defines subscriptions to Excel events.
	Events    []Event       `yaml:"events"`
	// Rtd contains configuration for the Real-Time Data server.
	Rtd       RtdConfig     `yaml:"rtd"`
}

// RtdConfig configures the Real-Time Data server.
type RtdConfig struct {
	// Enabled determines if the RTD server is enabled.
	Enabled     bool   `yaml:"enabled"`
	// ProgID is the Program ID for the RTD server (e.g., "MyProject.RTD").
	ProgID      string `yaml:"prog_id"`
	// Clsid is the Class ID for the RTD server (optional, generated if empty).
	Clsid       string `yaml:"clsid"`
	// Description is the description of the RTD server.
	Description string `yaml:"description"`
	// ThrottleInterval is the minimum time in milliseconds between RTD updates from Excel.
	// A value of 0 allows Excel to update as frequently as possible.
	// If unset, Excel's default (typically 2000ms) is used.
	ThrottleInterval *int `yaml:"throttle_interval,omitempty"`
}

// CacheConfig configures the global caching behavior.
type CacheConfig struct {
	// Enabled determines if caching is enabled globally.
	Enabled bool   `yaml:"enabled"`
	// TTL is the default Time-To-Live for cached items (e.g., "10m").
	TTL     string `yaml:"ttl"`
	// Jitter is the random variation applied to TTL (e.g., "1m").
	Jitter  string `yaml:"jitter"`
}

// LoggingConfig configures logging behavior.
type LoggingConfig struct {
	// Level is the log level (debug, info, warn, error).
	Level string `yaml:"level"`
	// Dir is the log directory.
	Dir   string `yaml:"dir"`
}

// Event defines a subscription to a specific Excel event.
type Event struct {
	// Type is the event type (e.g., "CalculationEnded").
	Type string `yaml:"type"`
	// Handler is the name of the function to invoke when the event occurs.
	Handler string `yaml:"handler"`
}

// BuildConfig encapsulates build-related settings.
type BuildConfig struct {
	// Singlefile configuration controls the embedding strategy.
	// Options: "xll" (embed Go server in XLL), "exe" (reserved/unimplemented), or empty (no embedding).
	Singlefile string `yaml:"singlefile"`
	// TempDir is the directory where embedded binaries are extracted (supports env vars).
	TempDir    string `yaml:"temp_dir"`
}

// ServerConfig configures the runtime behavior of the Go server.
type ServerConfig struct {
	// Command is the command to launch the server (e.g., "path/to/server").
	// Supports "${BIN}" placeholder for the full path of the server executable.
	// If you need to pass arguments, you must wrap "${BIN}" or the path in quotes (e.g., "\"${BIN}\" --arg").
	Command string `yaml:"command"`
	// Workers determines the size of the worker pool for handling requests.
	// If 0, defaults to runtime.NumCPU().
	Workers int `yaml:"workers"`
	// Timeout is the default execution timeout for synchronous functions (e.g., "2s").
	Timeout string `yaml:"timeout"`
	// AsyncAckTimeout is the timeout for receiving an initial ACK for async requests (e.g., "200ms").
	AsyncAckTimeout string `yaml:"async_ack_timeout"`
	// Launch controls whether the XLL automatically launches the server process.
	Launch  *LaunchConfig `yaml:"launch"`
}

// LaunchConfig controls the automatic process launching behavior.
type LaunchConfig struct {
	// Enabled, if true (default), causes the XLL to spawn the server process.
	Enabled *bool `yaml:"enabled"`
}

// ProjectConfig contains project metadata.
type ProjectConfig struct {
	// Name is the name of the project.
	Name string `yaml:"name"`
	// Version is the project version string.
	Version string `yaml:"version"`
}

// GenConfig controls the code generation process.
type GenConfig struct {
	// Go contains Go-specific code generation settings.
	Go               GoConfig `yaml:"go"`
	// DisablePidSuffix, if true, prevents appending the PID to the shared memory name.
	DisablePidSuffix bool     `yaml:"disable_pid_suffix"`
}

// GoConfig contains Go-specific generation settings.
type GoConfig struct {
	// Module is the Go module name.
	Module string `yaml:"module"`
}

// Function represents a user-defined Excel function.
type Function struct {
	// Name is the name of the function as it will appear in Excel.
	Name        string `yaml:"name"`
	// Description is the help text for the function.
	Description string `yaml:"description"`
	// Args is the list of arguments for the function.
	Args        []Arg  `yaml:"args"`
	// Return is the return type of the function.
	Return      string `yaml:"return"`
	// Async indicates if the function is asynchronous.
	Async       bool   `yaml:"async"`
	// Resizable indicates if the function returns a dynamic array (Excel 365+).
	Resizable   bool   `yaml:"resizable"`
	// Volatile indicates if the function is volatile (recalculated on every sheet change).
	Volatile    bool   `yaml:"volatile"`
	// Category is the function category in the Excel function wizard.
	Category    string `yaml:"category"`
	// Shortcut is the keyboard shortcut for the function.
	Shortcut    string `yaml:"shortcut"`
	// HelpTopic is the help topic string.
	HelpTopic   string `yaml:"help_topic"`
	// Timeout is the execution timeout for this specific function.
	Timeout     string `yaml:"timeout"`
	// Caller indicates if the function requires information about the calling cell.
	Caller      bool                 `yaml:"caller"`
	// Mode determines the execution mode of the function (sync, async, rtd).
	// Supersedes the Async boolean.
	Mode        string               `yaml:"mode"`
	// Cache configures caching for this specific function.
	Cache       *FunctionCacheConfig `yaml:"cache"`
}

// FunctionCacheConfig configures caching for a specific function.
type FunctionCacheConfig struct {
	// Enabled overrides the global enabled setting.
	Enabled *bool  `yaml:"enabled"`
	// TTL overrides the global TTL.
	TTL     string `yaml:"ttl"`
}

// Arg represents a single argument of an Excel function.
type Arg struct {
	// Name is the name of the argument.
	Name        string `yaml:"name"`
	// Type is the data type of the argument (e.g., "int", "string", "fp", "any").
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
	if err := validateProjectName(config.Project.Name); err != nil {
		return err
	}

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

	for _, fn := range config.Functions {
		if fn.Mode != "" {
			switch strings.ToLower(fn.Mode) {
			case "sync", "async", "rtd":
				// ok
			default:
				return fmt.Errorf("function '%s': invalid mode '%s' (allowed: sync, async, rtd)", fn.Name, fn.Mode)
			}
		}
	}

	if config.Rtd.Enabled && config.Rtd.ProgID == "" {
		return fmt.Errorf("rtd.prog_id is required when rtd.enabled is true")
	}

	return nil
}

func validateProjectName(name string) error {
	if name == "" {
		return fmt.Errorf("project name cannot be empty")
	}
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
			return fmt.Errorf("project name must only contain alphanumeric characters, underscores, and hyphens")
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
	// Normalize Function Modes
	for i := range config.Functions {
		fn := &config.Functions[i]
		if fn.Mode == "" {
			if fn.Async {
				fn.Mode = "async"
			} else {
				fn.Mode = "sync"
			}
		} else {
			// Sync legacy Async flag with Mode
			if fn.Mode == "async" {
				fn.Async = true
			} else {
				fn.Async = false
			}
		}
	}

	if config.Build.TempDir == "" {
		config.Build.TempDir = "${TEMP}"
	}

	// Server Launch defaults
	if config.Server.Launch == nil {
		config.Server.Launch = &LaunchConfig{}
	}
	if config.Server.Launch.Enabled == nil {
		t := true
		config.Server.Launch.Enabled = &t
	}

	if config.Logging.Level == "" {
		config.Logging.Level = "info"
	}

	if config.Rtd.Enabled {
		if config.Rtd.Description == "" {
			config.Rtd.Description = config.Rtd.ProgID
		}
		if config.Rtd.Clsid == "" && config.Rtd.ProgID != "" {
			u := uuid.NewSHA1(uuid.NameSpaceDNS, []byte(config.Rtd.ProgID))
			config.Rtd.Clsid = "{" + u.String() + "}"
		}
	}
}
