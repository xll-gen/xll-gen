package config

import (
	"fmt"
	"math"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Config represents the top-level structure of the xll.yaml file.
// It maps directly to the YAML configuration provided by the user.
type Config struct {
	// Project contains project metadata.
	Project ProjectConfig `yaml:"project"`
	// Build contains build-specific configuration.
	Build BuildConfig `yaml:"build"`
	// Logging contains logging configuration.
	Logging LoggingConfig `yaml:"logging"`
	// Cache contains global caching configuration.
	Cache CacheConfig `yaml:"cache"`
	// Server contains configuration for the Go server process.
	Server ServerConfig `yaml:"server"`
	// Functions is a list of Excel functions to register.
	Functions []Function `yaml:"functions"`
	// Gen contains code generation configuration.
	Gen GenConfig `yaml:"gen"`
	// Events defines subscriptions to Excel events.
	Events []Event `yaml:"events"`
	// Rtd contains configuration for the Real-Time Data server.
	Rtd RtdConfig `yaml:"rtd"`
	// Commands is a list of Excel commands (macros) backed by Go handlers.
	Commands []Command `yaml:"commands"`
	// Ribbon declares the custom ribbon UI referencing Commands.
	Ribbon RibbonConfig `yaml:"ribbon"`
}

// RtdConfig configures the Real-Time Data server.
type RtdConfig struct {
	// Enabled determines if the RTD server is enabled.
	Enabled bool `yaml:"enabled"`
	// ProgID is the Program ID for the RTD server (e.g., "MyProject.RTD").
	ProgID string `yaml:"prog_id"`
	// Clsid is the Class ID for the RTD server (optional, generated if empty).
	Clsid string `yaml:"clsid"`
	// Description is the description of the RTD server.
	Description string `yaml:"description"`
	// ThrottleInterval, when set (duration string, e.g. "250ms"), makes the
	// XLL set Application.RTD.ThrottleInterval at xlAutoOpen. Excel's default
	// is 2s, which batches RTD pushes. CAUTION: this is a per-user,
	// registry-persisted Excel setting — it stays changed after the add-in
	// unloads, which is why it is opt-in and never touched when empty.
	ThrottleInterval string `yaml:"throttle_interval"`
	// LoadingPlaceholder is the project-wide default for what an RTD-backed cell
	// (mode:"rtd" or mode:"rtd-once") displays on its first paint, before the
	// first value arrives. A per-function loading_placeholder overrides this.
	// The recognized values are (case-insensitive):
	//   ""             -> same as "getting_data" (the default)
	//   "getting_data" -> #GETTING_DATA cell error
	//   "na"           -> #N/A cell error
	//   anything else  -> shown verbatim as text (e.g. "Loading...")
	// For rtd-once this is the wrapper's authoritative first paint (scalar/grid;
	// a numgrid FP12 cell cannot carry an error/string so it shows an empty 0x0
	// placeholder regardless). For plain rtd it is ConnectData's initial value
	// (replacing the legacy "Connecting..."); the brief pre-connect #N/A is
	// inherent to Excel's RTD and is not overridden, since the streaming wrapper
	// must return live values verbatim.
	LoadingPlaceholder string `yaml:"loading_placeholder"`
}

// RtdPlaceholderKind classifies a resolved rtd-once loading placeholder.
type RtdPlaceholderKind int

const (
	// PlaceholderGettingData renders the #GETTING_DATA cell error (default).
	PlaceholderGettingData RtdPlaceholderKind = iota
	// PlaceholderNA renders the #N/A cell error.
	PlaceholderNA
	// PlaceholderText renders verbatim text (the Text field).
	PlaceholderText
)

// RtdPlaceholder is the resolved first-paint placeholder for an rtd-once cell.
type RtdPlaceholder struct {
	Kind RtdPlaceholderKind
	// Text is the verbatim string to display; meaningful only when
	// Kind == PlaceholderText.
	Text string
}

// ResolveRtdPlaceholder computes the effective first-paint placeholder for one
// rtd-once function: the per-function loading_placeholder wins, then the global
// rtd.loading_placeholder, then the "getting_data" default. The two reserved
// keywords ("getting_data", "na") are matched case-insensitively; every other
// non-empty value is treated as verbatim display text.
func ResolveRtdPlaceholder(fn Function, rtd RtdConfig) RtdPlaceholder {
	v := strings.TrimSpace(fn.LoadingPlaceholder)
	if v == "" {
		v = strings.TrimSpace(rtd.LoadingPlaceholder)
	}
	switch strings.ToLower(v) {
	case "", "getting_data":
		return RtdPlaceholder{Kind: PlaceholderGettingData}
	case "na":
		return RtdPlaceholder{Kind: PlaceholderNA}
	default:
		return RtdPlaceholder{Kind: PlaceholderText, Text: v}
	}
}

// CacheConfig configures the global caching behavior.
type CacheConfig struct {
	// Enabled determines if caching is enabled globally.
	Enabled bool `yaml:"enabled"`
	// TTL is the default Time-To-Live for cached items (e.g., "10m").
	TTL string `yaml:"ttl"`
	// Jitter is the random variation applied to TTL (e.g., "1m").
	Jitter string `yaml:"jitter"`
}

// LoggingConfig configures logging behavior.
type LoggingConfig struct {
	// Level is the log level (debug, info, warn, error).
	Level string `yaml:"level"`
	// Dir is the log directory.
	Dir string `yaml:"dir"`
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
	TempDir string `yaml:"temp_dir"`
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
	Launch *LaunchConfig `yaml:"launch"`
	// Chunk configures the runtime ChunkManager (reassembly buffer cap, sweep
	// cadence, idle TTL). All fields are optional; omitting Chunk or any of
	// its sub-fields leaves the corresponding ChunkManager defaults in
	// effect (see pkg/server/manager.go: Default* constants).
	Chunk *ChunkConfig `yaml:"chunk"`
}

// ChunkConfig is the YAML-facing knob for runtime chunked-message handling.
// Fields map 1:1 onto pkg/server.ChunkManager — see that type for semantics.
// Validation runs in Validate(); ApplyDefaults leaves zeros so the Go-side
// constants remain the single source of truth for defaults.
type ChunkConfig struct {
	// MaxBufferBytes caps per-transfer reassembly allocations (DoS guard).
	// Zero means pkg/server.DefaultMaxChunkBufferBytes (256 MiB).
	MaxBufferBytes int64 `yaml:"max_buffer_bytes"`
	// CleanupInterval is the sweep cadence (e.g. "30s"). Zero/empty means
	// pkg/server.DefaultCleanupInterval.
	CleanupInterval string `yaml:"cleanup_interval"`
	// BufferTTL is the idle window before a buffer is evicted (e.g. "60s").
	// Zero/empty means pkg/server.DefaultChunkBufferTTL.
	BufferTTL string `yaml:"buffer_ttl"`
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
	Go GoConfig `yaml:"go"`
	// DisablePidSuffix, if true, prevents appending the PID to the shared memory name.
	DisablePidSuffix bool `yaml:"disable_pid_suffix"`
}

// GoConfig contains Go-specific generation settings.
type GoConfig struct {
	// Module is the Go module name.
	Module string `yaml:"module"`
}

// Function represents a user-defined Excel function.
type Function struct {
	// Name is the name of the function as it will appear in Excel.
	Name string `yaml:"name"`
	// Description is the help text for the function.
	Description string `yaml:"description"`
	// Args is the list of arguments for the function.
	Args []Arg `yaml:"args"`
	// Return is the return type of the function.
	Return string `yaml:"return"`
	// Async indicates if the function is asynchronous.
	Async bool `yaml:"async"`
	// Resizable indicates if the function returns a dynamic array (Excel 365+).
	Resizable bool `yaml:"resizable"`
	// Volatile indicates if the function is volatile (recalculated on every sheet change).
	Volatile bool `yaml:"volatile"`
	// Category is the function category in the Excel function wizard.
	Category string `yaml:"category"`
	// Shortcut is the keyboard shortcut for the function.
	Shortcut string `yaml:"shortcut"`
	// HelpTopic is the help topic string.
	HelpTopic string `yaml:"help_topic"`
	// Timeout is the execution timeout for this specific function.
	Timeout string `yaml:"timeout"`
	// Caller indicates if the function requires information about the calling
	// cell. The C++ wrapper calls xlfCaller (callable from ANY XLL function)
	// and reports the caller's position (range rects) to the Go handler via
	// Range. By itself it is POSITION-ONLY: the caller's number-format string
	// (Range.Format()) is fetched via the macro-only xlfGetCell, which requires
	// the function to also be registered as a macro-sheet equivalent — see
	// Macro. caller:true alone stays thread-safe ('$').
	Caller bool `yaml:"caller"`
	// Macro registers the function as a macro-sheet equivalent ('#'), granting
	// macro-level C-API access inside the C++ wrapper — e.g. the caller
	// number-format fetch (xlfGetCell), which populates Range.Format(). The cost
	// is that Excel rejects '#' combined with the thread-safe '$' marker, so a
	// macro:true function is NOT registered thread-safe. NOTE: this does NOT
	// make Excel's COM object model writable from Go handlers during
	// calculation; sheet writes belong in commands or ScheduleSet.
	Macro bool `yaml:"macro"`
	// Mode determines the execution mode of the function (sync, async, rtd,
	// rtd-once). Supersedes the Async boolean.
	Mode string `yaml:"mode"`
	// Memoize is valid ONLY with mode:"rtd-once". When false (default), a
	// completed one-shot result is cleared at the end of the calculation
	// cycle (CalculationEnded/Canceled) so the next user-initiated recalc
	// (F9) recomputes — restoring normal worksheet semantics. When true, the
	// completed result persists until the add-in unloads (xlAutoClose),
	// turning the function into an implicit per-(name+args) memoization cache.
	Memoize bool `yaml:"memoize"`
	// MemoizeTTL is valid ONLY with mode:"rtd-once" and is the middle ground
	// between the default "once" lifecycle and memoize:true. When set (a Go
	// duration string, e.g. "30s", "5m"), a completed one-shot result is
	// reused for recalcs within the TTL; once the TTL has elapsed the next
	// call recomputes fresh. Mutually exclusive with Memoize (the TTL IS the
	// intermediate option). Must parse to a positive duration.
	MemoizeTTL string `yaml:"memoize_ttl"`
	// LoadingPlaceholder is valid ONLY with an RTD-backed mode (rtd, rtd-once).
	// It overrides the project-wide rtd.loading_placeholder for this one
	// function, controlling what the cell shows on its first paint before the
	// first value arrives. See RtdConfig.LoadingPlaceholder for the recognized
	// values and the per-mode caveats.
	LoadingPlaceholder string `yaml:"loading_placeholder"`
	// Cache configures caching for this specific function.
	Cache *FunctionCacheConfig `yaml:"cache"`
}

// FunctionCacheConfig configures caching for a specific function.
type FunctionCacheConfig struct {
	// Enabled overrides the global enabled setting.
	Enabled *bool `yaml:"enabled"`
	// TTL overrides the global TTL.
	TTL string `yaml:"ttl"`
}

// Arg represents a single argument of an Excel function.
type Arg struct {
	// Name is the name of the argument.
	Name string `yaml:"name"`
	// Type is the data type of the argument (e.g., "int", "string", "fp", "any").
	Type string `yaml:"type"`
	// Description is the help text for the argument.
	Description string `yaml:"description"`
}

// Command represents a user-defined Excel command (macro), invocable from
// ribbon buttons, a Ctrl+Shift shortcut, or by typing its name in the
// Alt+F8 dialog (XLL commands are runnable there but not listed).
type Command struct {
	// Name is the command name registered with Excel (xlfRegister, macroType=2).
	Name string `yaml:"name"`
	// Description is the help text for the command.
	Description string `yaml:"description"`
	// Handler is the Go method name on XllService. Defaults to Name.
	Handler string `yaml:"handler"`
	// Shortcut is a single letter; Excel binds it as Ctrl+Shift+<letter>.
	Shortcut string `yaml:"shortcut"`
}

// RibbonButton is one button in a structured-mode ribbon group.
type RibbonButton struct {
	// Label is the button caption.
	Label string `yaml:"label"`
	// Command is the name of the Command this button invokes (onAction).
	Command string `yaml:"command"`
	// Size is "large" or "normal" (default "normal").
	Size string `yaml:"size"`
	// Image is an imageMso name (e.g. "HappyFace") or a path to a
	// PNG/JPG/JPEG/BMP/GIF/ICO file relative to xll.yaml. File images are
	// embedded into the XLL and served via the loadImage ribbon callback.
	Image string `yaml:"image"`
}

// ribbonImageFileExts are the formats the runtime decoder (GDI+) accepts for
// ribbon button image files.
var ribbonImageFileExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".bmp": true, ".gif": true, ".ico": true,
}

// ClassifyRibbonImage reports how a ribbon button image value is interpreted:
// a file path (embedded into the XLL, served via loadImage) or a built-in
// imageMso name. A path-like value (contains / or \) with an unsupported
// extension is an error; imageMso names never contain separators or dots.
func ClassifyRibbonImage(value string) (isFile bool, err error) {
	if value == "" {
		return false, nil
	}
	// Extension after the last dot of the last path segment, either separator.
	base := value
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	ext := strings.ToLower(path.Ext(base))
	if ribbonImageFileExts[ext] {
		return true, nil
	}
	if strings.ContainsAny(value, `/\`) {
		return false, fmt.Errorf("ribbon image %q looks like a file path but has an unsupported extension (supported: .png .jpg .jpeg .bmp .gif .ico)", value)
	}
	return false, nil
}

// RibbonGroup is one group of buttons in a structured-mode ribbon tab.
type RibbonGroup struct {
	// Label is the group caption.
	Label string `yaml:"label"`
	// Buttons is the list of buttons in this group.
	Buttons []RibbonButton `yaml:"buttons"`
}

// RibbonConfig declares the add-in's custom ribbon UI. Two mutually
// exclusive modes: structured (Tab + Groups, XML is generated) or raw
// (XML names a customUI XML file authored by the user).
type RibbonConfig struct {
	// Tab is the custom tab label (structured mode).
	Tab string `yaml:"tab"`
	// Groups are the button groups under Tab (structured mode).
	Groups []RibbonGroup `yaml:"groups"`
	// XML is a path to a raw customUI XML file, relative to xll.yaml (raw mode).
	XML string `yaml:"xml"`
	// ProgID identifies the COM add-in helper (default "<project>.Ribbon").
	ProgID string `yaml:"prog_id"`
	// Clsid is the helper's class ID (derived from ProgID if empty).
	Clsid string `yaml:"clsid"`
}

// Enabled reports whether a ribbon UI was declared in either mode.
func (r RibbonConfig) Enabled() bool {
	return r.Tab != "" || r.XML != ""
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
	// "date" rides the double request path: Excel sends the serial as a double
	// and the generated server decodes it to a time.Time via
	// server.SerialToTime. Argument-only for now — a date RETURN would require
	// time.Time→serial encoding in the response path (not yet wired).
	"date": true,
}

// validReturnTypes is the set of allowed return types in xll.yaml.
//
// Scalars plus "any": the generated Go server serializes scalar returns
// directly and "any" returns through the canonical Go-value→protocol.Any
// mapping (handlers return a plain Go any; see pkg/server.BuildAnyFromGo).
//
// "grid" and "numgrid" are return-capable for sync/async functions: the
// generated server serializes the handler's [][]any / [][]float64 via
// pkg/server.BuildGridFromGo / BuildNumGridFromGo, and the C++ wrapper
// converts the response Grid/NumGrid into an xltypeMulti / FP12 XLOPER12
// (GridToXLOPER12 / NumGridToFP12). On Excel 2021+/365 an xltypeMulti
// returned by a `Q`-registered function (and an FP12 returned by a
// `K%`-registered function) spills natively into the surrounding cells; on
// pre-dynamic-array Excel the user gets the top-left cell (or must enter the
// formula as a legacy CSE array). No version detection or registration flag
// is required.
//
// "range" stays arg-only: returning a live reference (a `U`-coded return) is
// rejected by Excel (the worksheet name resolves to #NAME?, see AGENTS.md
// §19.2), and a value-position range has no meaningful spill semantics.
var validReturnTypes = map[string]bool{
	"int":     true,
	"float":   true,
	"string":  true,
	"bool":    true,
	"any":     true,
	"grid":    true,
	"numgrid": true,
}

// validEventTypes is the set of Excel event types wired end-to-end (C++
// registration via lookupEventCode AND server dispatch via lookupEventId).
// Growing this set requires touching both funcmap lookups and the
// non-builtin dispatch block in server.go.tmpl.
var validEventTypes = map[string]bool{
	"CalculationEnded":    true,
	"CalculationCanceled": true,
}

// rtdCompositeReturnTypes are composite return types rejected for the RTD
// modes (rtd / rtd-once). Unlike sync/async, grid/numgrid cannot ride the RTD
// push path (RtdUpdate's Any union would stringify them), so they are rejected
// here even though sync/async now serialize them as spilling returns.
var rtdCompositeReturnTypes = map[string]bool{
	"range":   true,
	"grid":    true,
	"numgrid": true,
}

// goReservedWords are the Go keywords. A function/argument/handler name that is
// a Go keyword is emitted verbatim as a Go identifier (interface method params,
// dispatch, handler names) and would be a syntax error.
var goReservedWords = map[string]bool{
	"break": true, "case": true, "chan": true, "const": true, "continue": true,
	"default": true, "defer": true, "else": true, "fallthrough": true, "for": true,
	"func": true, "go": true, "goto": true, "if": true, "import": true,
	"interface": true, "map": true, "package": true, "range": true, "return": true,
	"select": true, "struct": true, "switch": true, "type": true, "var": true,
}

// cppReservedWords are C++ keywords (C++17). A function/argument name that is a
// C++ keyword is emitted verbatim as a C++ identifier (wrapper params,
// registration) and would be a syntax error. Matched case-sensitively so a
// capitalized name like "New"/"Class" (not a keyword) is not falsely rejected.
var cppReservedWords = map[string]bool{
	"alignas": true, "alignof": true, "and": true, "and_eq": true, "asm": true,
	"auto": true, "bitand": true, "bitor": true, "bool": true, "break": true,
	"case": true, "catch": true, "char": true, "char16_t": true, "char32_t": true,
	"class": true, "compl": true, "const": true, "constexpr": true, "const_cast": true,
	"continue": true, "decltype": true, "default": true, "delete": true, "do": true,
	"double": true, "dynamic_cast": true, "else": true, "enum": true, "explicit": true,
	"export": true, "extern": true, "false": true, "float": true, "for": true,
	"friend": true, "goto": true, "if": true, "inline": true, "int": true,
	"long": true, "mutable": true, "namespace": true, "new": true, "noexcept": true,
	"not": true, "not_eq": true, "nullptr": true, "operator": true, "or": true,
	"or_eq": true, "private": true, "protected": true, "public": true, "register": true,
	"reinterpret_cast": true, "return": true, "short": true, "signed": true, "sizeof": true,
	"static": true, "static_assert": true, "static_cast": true, "struct": true, "switch": true,
	"template": true, "this": true, "thread_local": true, "throw": true, "true": true,
	"try": true, "typedef": true, "typeid": true, "typename": true, "union": true,
	"unsigned": true, "using": true, "virtual": true, "void": true, "volatile": true,
	"wchar_t": true, "while": true, "xor": true, "xor_eq": true,
}

// validateIdentifier checks a name destined to be emitted verbatim as a
// generated Go and/or C++ identifier (and registered with Excel): non-empty,
// [A-Za-z0-9_]+, no leading digit, and not a Go or C++ reserved word. `kind` is
// a human label for the error message (e.g. "command", "function",
// "argument"). Extracted from the original command-name validation so
// functions, arguments, and handlers get the same guard.
func validateIdentifier(kind, name string) error {
	if name == "" {
		return fmt.Errorf("%s name cannot be empty", kind)
	}
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			return fmt.Errorf("%s '%s': name must match [A-Za-z0-9_]+ (it is emitted into generated Go/C++ identifiers and registered with Excel via xlfRegister)", kind, name)
		}
	}
	if name[0] >= '0' && name[0] <= '9' {
		return fmt.Errorf("%s '%s': name must not start with a digit", kind, name)
	}
	if goReservedWords[name] {
		return fmt.Errorf("%s '%s': name is a Go reserved word (it is emitted verbatim as a Go identifier in the generated server/interface)", kind, name)
	}
	if cppReservedWords[name] {
		return fmt.Errorf("%s '%s': name is a C++ reserved word (it is emitted verbatim as a C++ identifier in the generated wrapper)", kind, name)
	}
	return nil
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

	if err := validateBuild(config); err != nil {
		return err
	}
	if err := validateEvents(config); err != nil {
		return err
	}
	if err := validateFunctionReturns(config); err != nil {
		return err
	}
	if err := validateLogging(config); err != nil {
		return err
	}
	if err := validateFunctionModes(config); err != nil {
		return err
	}
	if err := validateServerTimeouts(config); err != nil {
		return err
	}
	if err := validateRtd(config); err != nil {
		return err
	}
	if err := validateServerChunk(config); err != nil {
		return err
	}
	cmdNames, err := validateCommands(config)
	if err != nil {
		return err
	}
	if err := validateRibbon(config, cmdNames); err != nil {
		return err
	}
	return nil
}

// validateBuild checks the build.singlefile mode.
func validateBuild(config *Config) error {
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
	return nil
}

// validateEvents rejects duplicate and unsupported event types.
func validateEvents(config *Config) error {
	seenEvents := make(map[string]bool)
	for _, evt := range config.Events {
		if seenEvents[evt.Type] {
			return fmt.Errorf("duplicate event type: %s", evt.Type)
		}
		seenEvents[evt.Type] = true
		// Only the two calculation events are wired end-to-end: the C++ side
		// (lookupEventCode) and the server dispatch (lookupEventId) both map
		// unknown types to 0, which would register nothing and generate an
		// unreachable `case 0:` — reject at config time instead.
		if !validEventTypes[evt.Type] {
			return fmt.Errorf("event type '%s' is not supported (allowed: %s)", evt.Type, allowedTypesList(validEventTypes))
		}
		// Handler is dispatched as handler.<Handler>(ctx); defaulted to On<Type>
		// by ApplyDefaults, but validate it when explicitly set (a direct Validate
		// call may run without ApplyDefaults).
		if evt.Handler != "" {
			if err := validateIdentifier("event handler", evt.Handler); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateFunctionReturns checks each function's return type (per mode) and
// argument types. Kept as a separate pass from validateFunctionModes to
// preserve the original error-reporting order (return/arg errors surface before
// mode/lifecycle errors across the whole function list).
func validateFunctionReturns(config *Config) error {
	for _, fn := range config.Functions {
		// Plain mode:"rtd" and mode:"rtd-once" accept BOTH scalar and composite
		// (range/grid/numgrid/any) arguments. Scalar args are stringified into
		// the RTD topic; composite args travel the content-hash payload path
		// (AGENTS.md §19.3): the C++ wrapper computes a deterministic content
		// hash, sends the serialized payload once per calc cycle over the
		// normal SHM SetRefCache path, and embeds only the hash token ("h:<hex>")
		// in the topic string. Topic identity then tracks CONTENT — same grid →
		// same topic, edited grid → new hash → fresh compute — which both
		// delivers the contents to the Go handler AND fixes the old
		// "[Complex]" topic-identity collision.
		//
		// The RETURN side is unchanged: the push path (pkg/rtd → fbany.MapGo /
		// RtdUpdate's Any union) carries scalars and "any" only, so composite
		// RETURNS stay rejected for both RTD modes.
		isRtd := strings.EqualFold(fn.Mode, "rtd")
		isRtdOnce := strings.EqualFold(fn.Mode, "rtd-once")
		if isRtd {
			// Return: scalar or "any" only (the RTD push path carries scalars
			// and "any"; composites would be fmt.Sprintf-stringified). grid/
			// numgrid are sync/async-spillable returns but NOT valid here, so
			// reject them explicitly (they are now in validReturnTypes).
			if rtdCompositeReturnTypes[fn.Return] {
				return fmt.Errorf("function '%s': mode:\"rtd\" cannot return composite type '%s' (the RTD push path carries scalars and \"any\" only — a composite return would be stringified via fmt.Sprintf); return a scalar or \"any\", or use sync/async for spilling grid/numgrid returns", fn.Name, fn.Return)
			}
			if !validReturnTypes[fn.Return] {
				return fmt.Errorf("function '%s': return type '%s' is not supported (allowed: %s)", fn.Name, fn.Return, allowedTypesList(validReturnTypes))
			}
			// Composite/any ARGS are now supported via the content-hash payload
			// path (no per-arg rejection here).
		} else if isRtdOnce {
			// rtd-once may return scalar, "any", grid, or numgrid. grid/numgrid
			// spill via the RtdOnceGridRegistry path (the readiness signal rides
			// the RTD push; the array is returned through the normal calc path —
			// see the rtd-once-grid-spill design). "range" stays unsupported as a
			// return type: a value-position range return is meaningless and a
			// `U`-coded return breaks Excel registration (AGENTS.md §19.2).
			if fn.Return == "range" {
				return fmt.Errorf("function '%s': mode:\"rtd-once\" cannot return \"range\" (range is not a return type; return grid/numgrid for a spilling array instead)", fn.Name)
			}
			if !validReturnTypes[fn.Return] {
				return fmt.Errorf("function '%s': return type '%s' is not supported (allowed: %s)", fn.Name, fn.Return, allowedTypesList(validReturnTypes))
			}
			// Composite/any ARGS are now supported via the content-hash payload
			// path (no per-arg rejection here).
		} else if !validReturnTypes[fn.Return] {
			// grid/numgrid are now valid sync/async returns (they spill). Only
			// "range" remains arg-only: a value-position range return is
			// meaningless and a reference-position (`U`) return breaks Excel
			// registration (#NAME?, AGENTS.md §19.2).
			if fn.Return == "range" {
				return fmt.Errorf("function '%s': 'range' is supported as an argument type but not as a return type (returning a live reference is not meaningful — a `U`-coded return breaks Excel registration; return grid/numgrid for a spilling array instead)", fn.Name)
			}
			return fmt.Errorf("function '%s': return type '%s' is not supported (allowed: %s)", fn.Name, fn.Return, allowedTypesList(validReturnTypes))
		}
		seenArgs := make(map[string]bool)
		for _, arg := range fn.Args {
			if err := validateIdentifier(fmt.Sprintf("function '%s' argument", fn.Name), arg.Name); err != nil {
				return err
			}
			// flatc camelizes snake_case field names in its Go accessor
			// (start_date -> StartDate()), but the generator addresses the arg via
			// the template `capitalize` helper (start_date -> Start_date()), so an
			// underscore in an arg name makes the generated server reference a
			// method flatc never emitted -> Go compile failure. Reject underscores
			// in arg names (they are safe in function/command names, which do not
			// ride flatc field accessors).
			if strings.Contains(arg.Name, "_") {
				return fmt.Errorf("function '%s' argument '%s': argument names cannot contain '_' (flatc camelizes the FlatBuffers accessor, e.g. start_date -> StartDate(), which the generated server would not match); use lowerCamelCase", fn.Name, arg.Name)
			}
			if seenArgs[arg.Name] {
				return fmt.Errorf("function '%s': duplicate argument name '%s'", fn.Name, arg.Name)
			}
			seenArgs[arg.Name] = true
			if !validArgTypes[arg.Type] {
				// Special error message for nullable scalar types
				if strings.HasSuffix(arg.Type, "?") {
					return fmt.Errorf("function '%s' argument '%s': type '%s' is not supported (optional scalars are not supported by Excel API; use 'any' or 'scalar' instead)", fn.Name, arg.Name, arg.Type)
				}
				return fmt.Errorf("function '%s' argument '%s': type '%s' is not supported (allowed: %s)", fn.Name, arg.Name, arg.Type, allowedTypesList(validArgTypes))
			}
		}
	}

	return nil
}

// validateLogging checks the logging.level enum.
func validateLogging(config *Config) error {
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

// validateFunctionModes checks each function's name collision, execution mode,
// rtd-once requirements, memoize/memoize_ttl/loading_placeholder/caller/macro
// compatibility, and timeout. Second function pass (see validateFunctionReturns).
func validateFunctionModes(config *Config) error {
	seenFuncs := make(map[string]bool)
	for _, fn := range config.Functions {
		// Identifier legality: the name becomes an exported Go handler method, a
		// C++ wrapper symbol, and a flatc <Name>Request table — a bad charset,
		// leading digit, or reserved word breaks one of those. (Previously only
		// commands were charset-validated; a duplicate/empty function name only
		// surfaced late as flatc "type already exists".)
		if err := validateIdentifier("function", fn.Name); err != nil {
			return err
		}
		if seenFuncs[fn.Name] {
			return fmt.Errorf("duplicate function name: %s (the xlfRegister namespace and the flatc <Name>Request table are shared)", fn.Name)
		}
		seenFuncs[fn.Name] = true
		if msg := checkExcelNameCollision(fn.Name); msg != "" {
			return fmt.Errorf("function '%s': name %s", fn.Name, msg)
		}
		if fn.Mode != "" {
			switch strings.ToLower(fn.Mode) {
			case "sync", "async", "rtd", "rtd-once":
				// ok
			default:
				return fmt.Errorf("function '%s': invalid mode '%s' (allowed: sync, async, rtd, rtd-once)", fn.Name, fn.Mode)
			}
		}
		// rtd-once requires the RTD server (its result rides the RTD push
		// path). memoize is meaningful only for rtd-once.
		if strings.EqualFold(fn.Mode, "rtd-once") && !config.Rtd.Enabled {
			return fmt.Errorf("function '%s': mode:\"rtd-once\" requires rtd.enabled: true (the one-shot result is delivered through the RTD server)", fn.Name)
		}
		if fn.Memoize && !strings.EqualFold(fn.Mode, "rtd-once") {
			return fmt.Errorf("function '%s': memoize is only valid with mode:\"rtd-once\" (it controls the keep-vs-rerun lifecycle of the one-shot result)", fn.Name)
		}
		// memoize_ttl is the middle ground between "once" (default) and
		// memoize:true; it too is meaningful only for rtd-once, is mutually
		// exclusive with memoize:true, and must parse to a positive duration.
		if fn.MemoizeTTL != "" {
			if !strings.EqualFold(fn.Mode, "rtd-once") {
				return fmt.Errorf("function '%s': memoize_ttl is only valid with mode:\"rtd-once\" (it controls the keep-vs-rerun lifecycle of the one-shot result)", fn.Name)
			}
			if fn.Memoize {
				return fmt.Errorf("function '%s': memoize_ttl and memoize:true are mutually exclusive (memoize_ttl is the intermediate option: cache for the TTL then recompute; memoize:true caches until process teardown)", fn.Name)
			}
			d, err := parseDuration(fn.MemoizeTTL)
			if err != nil {
				return fmt.Errorf("function '%s': memoize_ttl: %w", fn.Name, err)
			}
			if d <= 0 {
				return fmt.Errorf("function '%s': memoize_ttl must be a positive duration, got %s", fn.Name, fn.MemoizeTTL)
			}
		}
		// loading_placeholder sets the RTD first-paint glyph, so it is meaningful
		// only for the RTD-backed modes (rtd, rtd-once). The global
		// rtd.loading_placeholder is a harmless no-op for projects with no
		// RTD-backed functions and is not validated here.
		if fn.LoadingPlaceholder != "" && !strings.EqualFold(fn.Mode, "rtd-once") && !strings.EqualFold(fn.Mode, "rtd") {
			return fmt.Errorf("function '%s': loading_placeholder is only valid with mode:\"rtd\" or mode:\"rtd-once\" (it sets the first-paint glyph shown before the first value arrives)", fn.Name)
		}
		// caller-aware is incompatible with rtd-once: the RTD wrapper routes
		// through xlfRtd (no xlfCaller/xlfGetCell call), and the handler runs
		// off the calc thread on a topic connect, so there is no caller cell
		// to report.
		if fn.Caller && strings.EqualFold(fn.Mode, "rtd-once") {
			return fmt.Errorf("function '%s': caller-aware functions are not supported with mode:\"rtd-once\" (the handler runs on a topic connect, not in the calling cell's calc)", fn.Name)
		}
		// macro mirrors caller's mode rules: it is the macro-sheet ('#')
		// registration that lets the caller wrapper call xlfGetCell, so it is
		// only meaningful where the wrapper runs in the calling cell's calc.
		// Reject it for rtd-once for the same reason caller is rejected (the
		// handler runs off the calc thread on a topic connect). It is allowed
		// for sync/async/rtd, exactly like caller.
		if fn.Macro && strings.EqualFold(fn.Mode, "rtd-once") {
			return fmt.Errorf("function '%s': macro:true (macro-sheet registration) is not supported with mode:\"rtd-once\" (the handler runs on a topic connect, not in the calling cell's calc, so the macro-level C-API is unreachable)", fn.Name)
		}
		if fn.Timeout != "" {
			// The RTD modes have no per-call timeout: the wrapper routes through
			// xlfRtd and the handler runs off the calc thread on a topic connect,
			// so the C++ rtd wrapper never consumes .Timeout. Worse, the generated
			// server declares `timeout_<Name>` for EVERY function but only USES it
			// in the non-rtd dispatch case, so an rtd(-once)+timeout config emits a
			// declared-and-not-used variable -> `go build` failure. Reject it.
			if strings.EqualFold(fn.Mode, "rtd") || strings.EqualFold(fn.Mode, "rtd-once") {
				return fmt.Errorf("function '%s': timeout is not supported with mode:%q (the RTD wrapper routes through xlfRtd and the handler runs off the calc thread on a topic connect — there is no per-call timeout to enforce)", fn.Name, fn.Mode)
			}
			if _, err := parseDuration(fn.Timeout); err != nil {
				return fmt.Errorf("function '%s': timeout: %w", fn.Name, err)
			}
		}
	}
	return nil
}

// validateServerTimeouts checks server.timeout and server.async_ack_timeout.
// Split from validateServerChunk (with validateRtd between them) to preserve
// the original error-reporting order.
func validateServerTimeouts(config *Config) error {
	if config.Server.Timeout != "" {
		if _, err := parseDuration(config.Server.Timeout); err != nil {
			return fmt.Errorf("server.timeout: %w", err)
		}
	}
	if config.Server.AsyncAckTimeout != "" {
		if _, err := parseDuration(config.Server.AsyncAckTimeout); err != nil {
			return fmt.Errorf("server.async_ack_timeout: %w", err)
		}
	}
	return nil
}

// validateRtd checks rtd.prog_id presence and rtd.throttle_interval bounds.
func validateRtd(config *Config) error {
	if config.Rtd.Enabled && config.Rtd.ProgID == "" {
		return fmt.Errorf("rtd.prog_id is required when rtd.enabled is true")
	}
	if config.Rtd.ThrottleInterval != "" {
		if !config.Rtd.Enabled {
			return fmt.Errorf("rtd.throttle_interval requires rtd.enabled: true")
		}
		d, err := parseDuration(config.Rtd.ThrottleInterval)
		if err != nil {
			return fmt.Errorf("rtd.throttle_interval: %w", err)
		}
		// Application.RTD.ThrottleInterval is a 32-bit millisecond count and
		// negative values are rejected by Excel.
		if d < 0 || d.Milliseconds() > math.MaxInt32 {
			return fmt.Errorf("rtd.throttle_interval must be between 0 and %dms, got %s", math.MaxInt32, config.Rtd.ThrottleInterval)
		}
	}
	return nil
}

// validateServerChunk checks the server.chunk tuning block.
func validateServerChunk(config *Config) error {
	if c := config.Server.Chunk; c != nil {
		if c.MaxBufferBytes < 0 {
			return fmt.Errorf("server.chunk.max_buffer_bytes must be non-negative, got %d", c.MaxBufferBytes)
		}
		if c.CleanupInterval != "" {
			if _, err := parseDuration(c.CleanupInterval); err != nil {
				return fmt.Errorf("server.chunk.cleanup_interval: %w", err)
			}
		}
		if c.BufferTTL != "" {
			if _, err := parseDuration(c.BufferTTL); err != nil {
				return fmt.Errorf("server.chunk.buffer_ttl: %w", err)
			}
		}
	}
	return nil
}

// validateCommands checks command names, collisions (with functions and each
// other), and shortcuts. It returns the set of valid command names for the
// ribbon validation that follows.
func validateCommands(config *Config) (map[string]bool, error) {
	fnNames := make(map[string]bool)
	for _, fn := range config.Functions {
		fnNames[fn.Name] = true
	}
	cmdNames := make(map[string]bool)
	seenShortcuts := make(map[string]string)
	for _, cmd := range config.Commands {
		if err := validateIdentifier("command", cmd.Name); err != nil {
			return nil, err
		}
		// Handler is the Go method name dispatched as handler.<Handler>; defaulted
		// to Name by ApplyDefaults, but validate it when explicitly set (a direct
		// Validate call may run without ApplyDefaults).
		if cmd.Handler != "" {
			if err := validateIdentifier("command handler", cmd.Handler); err != nil {
				return nil, err
			}
		}
		if fnNames[cmd.Name] {
			return nil, fmt.Errorf("command '%s' collides with a function of the same name (xlfRegister namespace is shared)", cmd.Name)
		}
		if msg := checkExcelNameCollision(cmd.Name); msg != "" {
			return nil, fmt.Errorf("command '%s': name %s", cmd.Name, msg)
		}
		if cmdNames[cmd.Name] {
			return nil, fmt.Errorf("duplicate command name: %s", cmd.Name)
		}
		cmdNames[cmd.Name] = true
		if cmd.Shortcut != "" {
			r := []rune(cmd.Shortcut)
			// ASCII letters only — xlfRegister's shortcut table is ASCII
			// (Ctrl+Shift+<letter>); do not "fix" with unicode.IsLetter.
			if len(r) != 1 || !((r[0] >= 'a' && r[0] <= 'z') || (r[0] >= 'A' && r[0] <= 'Z')) {
				return nil, fmt.Errorf("command '%s': shortcut must be a single letter (Excel binds it as Ctrl+Shift+<letter>), got %q", cmd.Name, cmd.Shortcut)
			}
			key := strings.ToUpper(cmd.Shortcut)
			if prev, ok := seenShortcuts[key]; ok {
				return nil, fmt.Errorf("command '%s': shortcut %q already used by command '%s'", cmd.Name, cmd.Shortcut, prev)
			}
			seenShortcuts[key] = cmd.Name
		}
	}
	return cmdNames, nil
}

// validateRibbon checks ribbon mode (structured vs raw-XML), that buttons
// reference known commands, and image/size validity.
func validateRibbon(config *Config, cmdNames map[string]bool) error {
	if config.Ribbon.Enabled() {
		if len(config.Commands) == 0 {
			return fmt.Errorf("ribbon requires at least one entry in 'commands'")
		}
		if config.Ribbon.XML != "" && (config.Ribbon.Tab != "" || len(config.Ribbon.Groups) > 0) {
			return fmt.Errorf("ribbon: 'xml' and 'tab'/'groups' are mutually exclusive")
		}
		for _, g := range config.Ribbon.Groups {
			for _, btn := range g.Buttons {
				if btn.Command == "" {
					return fmt.Errorf("ribbon button '%s': command is required", btn.Label)
				}
				if !cmdNames[btn.Command] {
					return fmt.Errorf("ribbon button '%s': unknown command '%s'", btn.Label, btn.Command)
				}
				if btn.Image != "" {
					if _, err := ClassifyRibbonImage(btn.Image); err != nil {
						return fmt.Errorf("ribbon button '%s': %w", btn.Label, err)
					}
				}
				switch btn.Size {
				case "", "normal", "large":
					// ok
				default:
					return fmt.Errorf("ribbon button '%s': invalid size '%s' (allowed: normal, large)", btn.Label, btn.Size)
				}
			}
		}
	} else if len(config.Ribbon.Groups) > 0 {
		return fmt.Errorf("ribbon: 'groups' requires 'tab' (structured mode)")
	}

	return nil
}

// parseDuration is a tiny wrapper around time.ParseDuration so Validate can
// surface YAML field name + parse error in one message. Kept private to
// match the existing config package surface — callers should not be
// re-parsing durations here.
func parseDuration(s string) (time.Duration, error) {
	return time.ParseDuration(s)
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
		// Validate() accepts mode case-insensitively; consumers (templates,
		// Async sync below) compare exact lowercase, so normalize here.
		fn.Mode = strings.ToLower(fn.Mode)
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

	for i := range config.Commands {
		if config.Commands[i].Handler == "" {
			config.Commands[i].Handler = config.Commands[i].Name
		}
	}

	// Mirror the Commands default: an event without an explicit handler
	// dispatches to On<Type> (e.g. CalculationEnded -> OnCalculationEnded),
	// matching the template-side fallbacks in getEventHandler.
	for i := range config.Events {
		if config.Events[i].Handler == "" {
			config.Events[i].Handler = "On" + config.Events[i].Type
		}
	}

	if config.Ribbon.Enabled() {
		if config.Ribbon.ProgID == "" {
			config.Ribbon.ProgID = config.Project.Name + ".Ribbon"
		}
		if config.Ribbon.Clsid == "" {
			u := uuid.NewSHA1(uuid.NameSpaceDNS, []byte(config.Ribbon.ProgID))
			config.Ribbon.Clsid = "{" + u.String() + "}"
		}
		for gi := range config.Ribbon.Groups {
			for bi := range config.Ribbon.Groups[gi].Buttons {
				if config.Ribbon.Groups[gi].Buttons[bi].Size == "" {
					config.Ribbon.Groups[gi].Buttons[bi].Size = "normal"
				}
			}
		}
	}
}
