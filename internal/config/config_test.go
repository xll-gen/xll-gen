package config

import (
	"strings"
	"testing"
)

func TestValidate_UnsupportedTypes(t *testing.T) {
	tests := []struct {
		name      string
		fnArgs    []Arg
		fnReturn  string
		wantError string
	}{
		{
			name: "string? argument",
			fnArgs: []Arg{
				{Name: "arg1", Type: "string?"},
			},
			fnReturn:  "string",
			wantError: "optional scalars are not supported",
		},
		{
			name:      "string? return",
			fnArgs:    []Arg{{Name: "a", Type: "int"}},
			fnReturn:  "string?",
			wantError: "type 'string?' is not supported",
		},
		{
			name:      "double argument (should be float)",
			fnArgs:    []Arg{{Name: "a", Type: "double"}},
			fnReturn:  "int",
			wantError: "type 'double' is not supported",
		},
		{
			name:      "int? argument (rejected)",
			fnArgs:    []Arg{{Name: "a", Type: "int?"}},
			fnReturn:  "int",
			wantError: "optional scalars are not supported",
		},
		{
			name:      "float? argument (rejected)",
			fnArgs:    []Arg{{Name: "a", Type: "float?"}},
			fnReturn:  "int",
			wantError: "optional scalars are not supported",
		},
		{
			name:      "int? return (rejected)",
			fnArgs:    []Arg{{Name: "a", Type: "int"}},
			fnReturn:  "int?",
			wantError: "type 'int?' is not supported",
		},
		{
			name:      "valid types",
			fnArgs:    []Arg{{Name: "a", Type: "string"}, {Name: "b", Type: "any"}, {Name: "c", Type: "range"}, {Name: "d", Type: "numgrid"}},
			fnReturn:  "bool",
			wantError: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Project: ProjectConfig{
					Name: "TestProject",
				},
				Functions: []Function{
					{
						Name:   "TestFunc",
						Args:   tt.fnArgs,
						Return: tt.fnReturn,
					},
				},
			}

			err := Validate(cfg)
			if tt.wantError != "" {
				if err == nil {
					t.Errorf("Validate() expected error containing %q, got nil", tt.wantError)
				} else if !strings.Contains(err.Error(), tt.wantError) {
					t.Errorf("Validate() error = %v, want substring %q", err, tt.wantError)
				}
			} else {
				if err != nil {
					t.Errorf("Validate() unexpected error: %v", err)
				}
			}
		})
	}
}

// TestValidate_CompositeReturnTypes locks in the spill-support return rules:
//   - grid/numgrid are ACCEPTED as sync/async return types (they spill in
//     dynamic-array Excel; the Go server serializes them via
//     pkg/server.BuildGridFromGo / BuildNumGridFromGo).
//   - range stays REJECTED as a return type (a value-position range is
//     meaningless; a `U`-coded reference return breaks Excel registration).
//   - all three stay REJECTED as RTD / RTD-once returns (the push path carries
//     scalars and "any" only).
//
// All three remain valid as ARGUMENT types. Scalar returns and "any" stay
// valid.
func TestValidate_CompositeReturnTypes(t *testing.T) {
	composite := []string{"range", "grid", "numgrid", "any"}
	rtdRejected := []string{"range", "grid", "numgrid"}

	// range is the only composite type still rejected as a sync/async return.
	t.Run("reject range return", func(t *testing.T) {
		cfg := &Config{
			Project:   ProjectConfig{Name: "TestProject"},
			Functions: []Function{{Name: "TestFunc", Return: "range"}},
		}
		err := Validate(cfg)
		if err == nil {
			t.Fatalf("Validate() expected error for return type %q, got nil", "range")
		}
		msg := err.Error()
		if !strings.Contains(msg, "not meaningful") {
			t.Errorf("Validate() error = %v, want explanation that returning a reference is not meaningful", err)
		}
		if !strings.Contains(msg, "range") {
			t.Errorf("Validate() error = %v, want mention of type %q", err, "range")
		}
	})

	// grid/numgrid are now ACCEPTED as sync AND async return types (spill).
	for _, typ := range []string{"grid", "numgrid"} {
		for _, mode := range []string{"sync", "async"} {
			t.Run("allow "+typ+" return ("+mode+")", func(t *testing.T) {
				cfg := &Config{
					Project: ProjectConfig{Name: "TestProject"},
					Functions: []Function{
						{Name: "TestFunc", Mode: mode, Async: mode == "async", Return: typ},
					},
				}
				if err := Validate(cfg); err != nil {
					t.Errorf("Validate() unexpected error for %s %q return: %v", mode, typ, err)
				}
			})
		}
	}

	// "any" must STILL be ACCEPTED as a RETURN type for RTD functions: the RTD
	// push path (pkg/rtd → fbany.MapGo) carries scalars and "any". The default
	// scaffold ships StockQuote (mode:"rtd", return:"any").
	t.Run("allow any return for rtd", func(t *testing.T) {
		cfg := &Config{
			Project: ProjectConfig{Name: "TestProject"},
			Functions: []Function{
				{Name: "TestFunc", Mode: "rtd", Return: "any"},
			},
		}
		if err := Validate(cfg); err != nil {
			t.Errorf("Validate() unexpected error for rtd return %q: %v", "any", err)
		}
	})

	// Composite returns are REJECTED for RTD: the push path stringifies a
	// composite via fmt.Sprintf. The message must explain the push-path limit.
	for _, typ := range rtdRejected {
		t.Run("reject "+typ+" return for rtd", func(t *testing.T) {
			cfg := &Config{
				Project: ProjectConfig{Name: "TestProject"},
				Functions: []Function{
					{Name: "TestFunc", Mode: "rtd", Return: typ},
				},
			}
			err := Validate(cfg)
			if err == nil {
				t.Fatalf("Validate() expected error for rtd composite return %q, got nil", typ)
			}
			msg := err.Error()
			if !strings.Contains(msg, "RTD push path") {
				t.Errorf("Validate() error = %v, want explanation about the RTD push path", err)
			}
			if !strings.Contains(msg, typ) {
				t.Errorf("Validate() error = %v, want mention of type %q", err, typ)
			}
		})
	}

	// Composite/any ARGS are now ACCEPTED for RTD: they travel the content-hash
	// payload path (the C++ wrapper hashes the content, ships the payload once
	// per cycle over SetRefCache, and embeds only the hash token in the topic).
	for _, typ := range composite {
		t.Run("allow "+typ+" arg for rtd", func(t *testing.T) {
			cfg := &Config{
				Project: ProjectConfig{Name: "TestProject"},
				Functions: []Function{
					{Name: "TestFunc", Mode: "rtd", Args: []Arg{{Name: "a", Type: typ}}, Return: "any"},
				},
			}
			if err := Validate(cfg); err != nil {
				t.Errorf("Validate() unexpected error for rtd composite/any arg %q: %v", typ, err)
			}
		})
	}

	// Scalar args remain valid for RTD.
	for _, typ := range []string{"int", "float", "string", "bool"} {
		t.Run("allow "+typ+" arg for rtd", func(t *testing.T) {
			cfg := &Config{
				Project: ProjectConfig{Name: "TestProject"},
				Functions: []Function{
					{Name: "TestFunc", Mode: "rtd", Args: []Arg{{Name: "a", Type: typ}}, Return: "any"},
				},
			}
			if err := Validate(cfg); err != nil {
				t.Errorf("Validate() unexpected error for rtd scalar arg %q: %v", typ, err)
			}
		})
	}

	// Each composite/any type must STILL be ACCEPTED as an argument type when
	// the return is scalar.
	for _, typ := range composite {
		t.Run("allow "+typ+" argument", func(t *testing.T) {
			cfg := &Config{
				Project: ProjectConfig{Name: "TestProject"},
				Functions: []Function{
					{Name: "TestFunc", Args: []Arg{{Name: "a", Type: typ}}, Return: "int"},
				},
			}
			if err := Validate(cfg); err != nil {
				t.Errorf("Validate() unexpected error for %q argument: %v", typ, err)
			}
		})
	}

	// Each scalar return type must remain valid, and "any" is valid as a
	// sync/async return (the generated server serializes the handler's Go
	// value through the canonical Go-value→protocol.Any mapping).
	for _, typ := range []string{"int", "float", "string", "bool", "any"} {
		t.Run("allow "+typ+" return", func(t *testing.T) {
			cfg := &Config{
				Project:   ProjectConfig{Name: "TestProject"},
				Functions: []Function{{Name: "TestFunc", Return: typ}},
			}
			if err := Validate(cfg); err != nil {
				t.Errorf("Validate() unexpected error for scalar return %q: %v", typ, err)
			}
		})
	}
}

func baseCmdCfg() *Config {
	return &Config{
		Project: ProjectConfig{Name: "demo"},
		Functions: []Function{
			{Name: "MyFunc", Return: "float"},
		},
	}
}

func TestCommandValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string // substring; "" = expect success
	}{
		{
			name: "valid command without ribbon",
			mutate: func(c *Config) {
				c.Commands = []Command{{Name: "RunReport"}}
			},
		},
		{
			name: "empty command name",
			mutate: func(c *Config) {
				c.Commands = []Command{{Name: ""}}
			},
			wantErr: "command name cannot be empty",
		},
		{
			name: "command name with double-quote rejected",
			mutate: func(c *Config) {
				c.Commands = []Command{{Name: `A"B`}}
			},
			wantErr: "must match",
		},
		{
			name: "command name with backslash rejected",
			mutate: func(c *Config) {
				c.Commands = []Command{{Name: `A\B`}}
			},
			wantErr: "must match",
		},
		{
			name: "command name with non-ascii rejected",
			mutate: func(c *Config) {
				c.Commands = []Command{{Name: "리포트"}}
			},
			wantErr: "must match",
		},
		{
			name: "command name starting with digit rejected",
			mutate: func(c *Config) {
				c.Commands = []Command{{Name: "1Run"}}
			},
			wantErr: "must not start",
		},
		{
			name: "command name collides with function name",
			mutate: func(c *Config) {
				c.Commands = []Command{{Name: "MyFunc"}}
			},
			wantErr: "collides",
		},
		{
			name: "duplicate command names",
			mutate: func(c *Config) {
				c.Commands = []Command{{Name: "A"}, {Name: "A"}}
			},
			wantErr: "duplicate command",
		},
		{
			name: "shortcut must be single letter",
			mutate: func(c *Config) {
				c.Commands = []Command{{Name: "A", Shortcut: "Ctrl+Shift+R"}}
			},
			wantErr: "shortcut",
		},
		{
			name: "non-ascii shortcut rejected",
			mutate: func(c *Config) {
				c.Commands = []Command{{Name: "A", Shortcut: "é"}}
			},
			wantErr: "shortcut",
		},
		{
			name: "duplicate shortcut across commands",
			mutate: func(c *Config) {
				c.Commands = []Command{{Name: "A", Shortcut: "r"}, {Name: "B", Shortcut: "R"}}
			},
			wantErr: "already used",
		},
		{
			name: "ribbon without commands",
			mutate: func(c *Config) {
				c.Ribbon = RibbonConfig{Tab: "Tools"}
			},
			wantErr: "ribbon requires",
		},
		{
			name: "ribbon structured and raw xml are mutually exclusive",
			mutate: func(c *Config) {
				c.Commands = []Command{{Name: "A"}}
				c.Ribbon = RibbonConfig{Tab: "Tools", XML: "ribbon.xml"}
			},
			wantErr: "mutually exclusive",
		},
		{
			name: "button with empty command",
			mutate: func(c *Config) {
				c.Commands = []Command{{Name: "A"}}
				c.Ribbon = RibbonConfig{Tab: "Tools", Groups: []RibbonGroup{
					{Label: "G", Buttons: []RibbonButton{{Label: "B", Command: ""}}},
				}}
			},
			wantErr: "command is required",
		},
		{
			name: "ribbon groups without tab",
			mutate: func(c *Config) {
				c.Commands = []Command{{Name: "A"}}
				c.Ribbon = RibbonConfig{Groups: []RibbonGroup{{Label: "G"}}}
			},
			wantErr: "requires 'tab'",
		},
		{
			name: "button references unknown command",
			mutate: func(c *Config) {
				c.Commands = []Command{{Name: "A"}}
				c.Ribbon = RibbonConfig{Tab: "Tools", Groups: []RibbonGroup{
					{Label: "G", Buttons: []RibbonButton{{Label: "B", Command: "Nope"}}},
				}}
			},
			wantErr: "unknown command",
		},
		{
			name: "invalid button size",
			mutate: func(c *Config) {
				c.Commands = []Command{{Name: "A"}}
				c.Ribbon = RibbonConfig{Tab: "Tools", Groups: []RibbonGroup{
					{Label: "G", Buttons: []RibbonButton{{Label: "B", Command: "A", Size: "huge"}}},
				}}
			},
			wantErr: "size",
		},
		{
			name: "valid structured ribbon",
			mutate: func(c *Config) {
				c.Commands = []Command{{Name: "A"}}
				c.Ribbon = RibbonConfig{Tab: "Tools", Groups: []RibbonGroup{
					{Label: "G", Buttons: []RibbonButton{{Label: "B", Command: "A", Size: "large"}}},
				}}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseCmdCfg()
			tt.mutate(cfg)
			err := Validate(cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

func TestCommandDefaults(t *testing.T) {
	cfg := baseCmdCfg()
	cfg.Commands = []Command{{Name: "RunReport"}}
	cfg.Ribbon = RibbonConfig{Tab: "Tools", Groups: []RibbonGroup{
		{Label: "G", Buttons: []RibbonButton{{Label: "B", Command: "RunReport"}}},
	}}
	ApplyDefaults(cfg)

	if cfg.Commands[0].Handler != "RunReport" {
		t.Errorf("handler default: got %q", cfg.Commands[0].Handler)
	}
	if cfg.Ribbon.ProgID != "demo.Ribbon" {
		t.Errorf("ribbon prog_id default: got %q", cfg.Ribbon.ProgID)
	}
	if cfg.Ribbon.Clsid == "" || cfg.Ribbon.Clsid[0] != '{' {
		t.Errorf("ribbon clsid not derived: got %q", cfg.Ribbon.Clsid)
	}
	// Deterministic: same ProgID -> same CLSID
	cfg2 := baseCmdCfg()
	cfg2.Commands = cfg.Commands
	cfg2.Ribbon = RibbonConfig{Tab: "Tools", Groups: cfg.Ribbon.Groups}
	ApplyDefaults(cfg2)
	if cfg2.Ribbon.Clsid != cfg.Ribbon.Clsid {
		t.Errorf("clsid not deterministic: %q vs %q", cfg.Ribbon.Clsid, cfg2.Ribbon.Clsid)
	}
	if cfg.Ribbon.Groups[0].Buttons[0].Size != "normal" {
		t.Errorf("button size default: got %q", cfg.Ribbon.Groups[0].Buttons[0].Size)
	}
}

func TestRibbonEnabled(t *testing.T) {
	r := RibbonConfig{}
	if r.Enabled() {
		t.Error("empty ribbon should be disabled")
	}
	if !(RibbonConfig{Tab: "T"}).Enabled() {
		t.Error("tab mode should be enabled")
	}
	if !(RibbonConfig{XML: "r.xml"}).Enabled() {
		t.Error("xml mode should be enabled")
	}
}

func TestValidate_ProjectName(t *testing.T) {
	tests := []struct {
		name      string
		projName  string
		wantError string
	}{
		{
			name:      "valid name",
			projName:  "MyProject_v1",
			wantError: "",
		},
		{
			name:      "valid name with hyphens",
			projName:  "my-awesome-project",
			wantError: "",
		},
		{
			name:      "invalid space",
			projName:  "My Project",
			wantError: "project name must only contain alphanumeric characters, underscores, and hyphens",
		},
		{
			name:      "invalid slash",
			projName:  "My/Project",
			wantError: "project name must only contain alphanumeric characters, underscores, and hyphens",
		},
		{
			name:      "invalid dot",
			projName:  "My.Project",
			wantError: "project name must only contain alphanumeric characters, underscores, and hyphens",
		},
		{
			name:      "empty name",
			projName:  "",
			wantError: "project name cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				Project: ProjectConfig{
					Name: tt.projName,
				},
				Functions: []Function{}, // Empty to avoid function validation errors
			}

			err := Validate(cfg)
			if tt.wantError != "" {
				if err == nil {
					t.Errorf("Validate() expected error containing %q, got nil", tt.wantError)
				} else if !strings.Contains(err.Error(), tt.wantError) {
					t.Errorf("Validate() error = %v, want substring %q", err, tt.wantError)
				}
			} else {
				if err != nil {
					t.Errorf("Validate() unexpected error: %v", err)
				}
			}
		})
	}
}

func TestClassifyRibbonImage(t *testing.T) {
	cases := []struct {
		value   string
		isFile  bool
		wantErr bool
	}{
		{"HappyFace", false, false},        // classic imageMso
		{"icon.png", true, false},          // bare filename with known ext
		{"icons/refresh.png", true, false}, // forward-slash path
		{`icons\refresh.PNG`, true, false}, // backslash path, case-insensitive ext
		{"./icons/a.jpeg", true, false},    // all supported exts are files
		{"a.bmp", true, false},
		{"a.gif", true, false},
		{"a.ico", true, false},
		{"icons/refresh.svg", false, true}, // path-like + unsupported ext -> error
		{"icons/refresh", false, true},     // path-like + no ext -> error
		{"weird.xyz", false, false},        // no separator, unknown ext -> imageMso
		{"", false, false},                 // empty -> not a file, no error
	}
	for _, c := range cases {
		isFile, err := ClassifyRibbonImage(c.value)
		if c.wantErr {
			if err == nil {
				t.Errorf("ClassifyRibbonImage(%q): expected error, got nil", c.value)
			}
			continue
		}
		if err != nil {
			t.Errorf("ClassifyRibbonImage(%q): unexpected error: %v", c.value, err)
			continue
		}
		if isFile != c.isFile {
			t.Errorf("ClassifyRibbonImage(%q) = %v, want %v", c.value, isFile, c.isFile)
		}
	}
}

func TestRibbonImageUnsupportedExtension(t *testing.T) {
	cfg := baseCmdCfg()
	cfg.Commands = []Command{{Name: "A"}}
	cfg.Ribbon = RibbonConfig{Tab: "Tools", Groups: []RibbonGroup{
		{Label: "G", Buttons: []RibbonButton{{Label: "B", Command: "A", Image: "icons/refresh.svg"}}},
	}}
	ApplyDefaults(cfg)
	err := Validate(cfg)
	if err == nil {
		t.Fatalf("expected error for unsupported ribbon image extension, got nil")
	}
}

// TestValidate_EventTypes locks in the supported-event whitelist: only the
// two calculation events are wired end-to-end (C++ lookupEventCode and server
// lookupEventId both map anything else to 0), so unknown types must be
// rejected at config time instead of generating broken registrations.
func TestValidate_EventTypes(t *testing.T) {
	valid := &Config{
		Project: ProjectConfig{Name: "TestProject"},
		Events: []Event{
			{Type: "CalculationEnded", Handler: "OnRecalc"},
			{Type: "CalculationCanceled"},
		},
	}
	if err := Validate(valid); err != nil {
		t.Errorf("Validate() unexpected error for builtin events: %v", err)
	}

	invalid := &Config{
		Project: ProjectConfig{Name: "TestProject"},
		Events:  []Event{{Type: "SheetActivated", Handler: "OnSheetActivated"}},
	}
	err := Validate(invalid)
	if err == nil {
		t.Fatal("Validate() expected error for unsupported event type, got nil")
	}
	if !strings.Contains(err.Error(), "event type 'SheetActivated' is not supported") {
		t.Errorf("Validate() error = %v, want unsupported-event message", err)
	}
}

// TestApplyDefaults_EventHandler verifies an event without an explicit
// handler defaults to On<Type>, mirroring the Commands handler default.
func TestApplyDefaults_EventHandler(t *testing.T) {
	cfg := &Config{
		Project: ProjectConfig{Name: "TestProject"},
		Events: []Event{
			{Type: "CalculationEnded"},
			{Type: "CalculationCanceled", Handler: "OnCancel"},
		},
	}
	ApplyDefaults(cfg)
	if got := cfg.Events[0].Handler; got != "OnCalculationEnded" {
		t.Errorf("Events[0].Handler = %q, want OnCalculationEnded", got)
	}
	if got := cfg.Events[1].Handler; got != "OnCancel" {
		t.Errorf("Events[1].Handler = %q, want OnCancel (explicit value must win)", got)
	}
}

// TestValidate_ExcelNameCollisions locks in the function/command name guard
// against (a) XLM macro keywords, which Excel rejects in worksheet formulas
// outright (showcase "Echo" bug), and (b) built-in worksheet functions, which
// silently shadow the XLL registration (showcase "IsEven" bug).
func TestValidate_ExcelNameCollisions(t *testing.T) {
	mk := func(fnName string) *Config {
		return &Config{
			Project:   ProjectConfig{Name: "TestProject"},
			Functions: []Function{{Name: fnName, Return: "int"}},
		}
	}

	if err := Validate(mk("Echo")); err == nil || !strings.Contains(err.Error(), "XLM") {
		t.Errorf("Echo (XLM keyword) must be rejected, got %v", err)
	}
	if err := Validate(mk("IsEven")); err == nil || !strings.Contains(err.Error(), "built-in") {
		t.Errorf("IsEven (built-in) must be rejected, got %v", err)
	}
	// Case-insensitive: Excel name resolution ignores case.
	if err := Validate(mk("xlookup")); err == nil {
		t.Error("xlookup (built-in, lowercase) must be rejected")
	}
	// Renamed showcase functions stay valid.
	for _, ok := range []string{"EchoAny", "IsEvenInt", "WhoAmI"} {
		if err := Validate(mk(ok)); err != nil {
			t.Errorf("%s must be valid, got %v", ok, err)
		}
	}

	// Commands share the guard.
	cmdCfg := &Config{
		Project:  ProjectConfig{Name: "TestProject"},
		Commands: []Command{{Name: "Beep"}},
	}
	if err := Validate(cmdCfg); err == nil || !strings.Contains(err.Error(), "XLM") {
		t.Errorf("command Beep (XLM keyword) must be rejected, got %v", err)
	}
}

// TestValidate_RtdThrottleInterval pins rtd.throttle_interval validation:
// requires rtd.enabled, must parse as a non-negative duration.
func TestValidate_RtdThrottleInterval(t *testing.T) {
	mk := func(enabled bool, throttle string) *Config {
		cfg := &Config{Project: ProjectConfig{Name: "TestProject"}}
		cfg.Rtd = RtdConfig{Enabled: enabled, ProgID: "P.Rtd", ThrottleInterval: throttle}
		return cfg
	}

	if err := Validate(mk(true, "250ms")); err != nil {
		t.Errorf("valid throttle rejected: %v", err)
	}
	if err := Validate(mk(true, "0s")); err != nil {
		t.Errorf("zero throttle must be allowed (Excel accepts 0): %v", err)
	}
	if err := Validate(mk(false, "250ms")); err == nil || !strings.Contains(err.Error(), "requires rtd.enabled") {
		t.Errorf("throttle without rtd.enabled must be rejected, got %v", err)
	}
	if err := Validate(mk(true, "fast")); err == nil {
		t.Error("unparseable throttle must be rejected")
	}
	if err := Validate(mk(true, "-1s")); err == nil {
		t.Error("negative throttle must be rejected")
	}
}

// TestValidate_RtdOnce pins the mode:"rtd-once" rules:
//   - accepted with scalar/any return + scalar OR composite args (rtd.enabled required)
//   - composite args accepted (content-hash payload path)
//   - non-scalar (composite) return rejected
//   - memoize accepted only with rtd-once
//   - caller-aware rejected with rtd-once
func TestValidate_RtdOnce(t *testing.T) {
	// Base config with RTD enabled and a single rtd-once function.
	mk := func(fn Function) *Config {
		return &Config{
			Project:   ProjectConfig{Name: "TestProject"},
			Rtd:       RtdConfig{Enabled: true, ProgID: "P.Rtd"},
			Functions: []Function{fn},
		}
	}

	// Accepted: scalar/any returns with scalar args.
	for _, ret := range []string{"int", "float", "string", "bool", "any"} {
		t.Run("accept return "+ret, func(t *testing.T) {
			cfg := mk(Function{
				Name:   "Compute",
				Mode:   "rtd-once",
				Return: ret,
				Args:   []Arg{{Name: "a", Type: "int"}, {Name: "b", Type: "string"}},
			})
			if err := Validate(cfg); err != nil {
				t.Fatalf("rtd-once with return %q + scalar args must be valid, got %v", ret, err)
			}
		})
	}

	// grid/numgrid returns are now ACCEPTED for rtd-once: they spill via the
	// RtdOnceGridRegistry path (see the rtd-once-grid-spill design). Only
	// "range" stays rejected as a return type.
	for _, ret := range []string{"grid", "numgrid"} {
		t.Run("accept return "+ret, func(t *testing.T) {
			cfg := mk(Function{Name: "Compute", Mode: "rtd-once", Return: ret})
			if err := Validate(cfg); err != nil {
				t.Fatalf("rtd-once with return %q must be allowed, got %v", ret, err)
			}
		})
	}
	t.Run("reject return range", func(t *testing.T) {
		cfg := mk(Function{Name: "Compute", Mode: "rtd-once", Return: "range"})
		err := Validate(cfg)
		if err == nil {
			t.Fatal("rtd-once with return \"range\" must still be rejected")
		}
	})

	// Composite/any args are now ACCEPTED for rtd-once: they travel the
	// content-hash payload path (the hash token also flows into the once-key,
	// making memoization content-addressed — same grid → cached result,
	// changed grid → fresh compute).
	for _, at := range []string{"range", "grid", "numgrid", "any"} {
		t.Run("allow arg "+at, func(t *testing.T) {
			cfg := mk(Function{
				Name:   "Compute",
				Mode:   "rtd-once",
				Return: "float",
				Args:   []Arg{{Name: "x", Type: at}},
			})
			if err := Validate(cfg); err != nil {
				t.Errorf("rtd-once with composite/any arg %q must be valid, got %v", at, err)
			}
		})
	}

	// memoize only valid on rtd-once.
	t.Run("memoize on rtd-once ok", func(t *testing.T) {
		cfg := mk(Function{Name: "Compute", Mode: "rtd-once", Return: "float", Memoize: true})
		if err := Validate(cfg); err != nil {
			t.Fatalf("memoize on rtd-once must be valid, got %v", err)
		}
	})
	for _, mode := range []string{"sync", "async", "rtd"} {
		t.Run("memoize rejected on "+mode, func(t *testing.T) {
			cfg := &Config{
				Project:   ProjectConfig{Name: "TestProject"},
				Rtd:       RtdConfig{Enabled: true, ProgID: "P.Rtd"},
				Functions: []Function{{Name: "F", Mode: mode, Return: "int", Memoize: true}},
			}
			// rtd return validates against the wider set; "int" is fine there.
			err := Validate(cfg)
			if err == nil || !strings.Contains(err.Error(), "memoize is only valid with mode:\"rtd-once\"") {
				t.Fatalf("memoize on %q must be rejected with the memoize message, got %v", mode, err)
			}
		})
	}

	// rtd-once requires rtd.enabled.
	t.Run("requires rtd.enabled", func(t *testing.T) {
		cfg := &Config{
			Project:   ProjectConfig{Name: "TestProject"},
			Functions: []Function{{Name: "Compute", Mode: "rtd-once", Return: "float"}},
		}
		err := Validate(cfg)
		if err == nil || !strings.Contains(err.Error(), "requires rtd.enabled") {
			t.Fatalf("rtd-once without rtd.enabled must be rejected, got %v", err)
		}
	})

	// caller-aware rejected with rtd-once.
	t.Run("caller rejected", func(t *testing.T) {
		cfg := mk(Function{Name: "Compute", Mode: "rtd-once", Return: "float", Caller: true})
		err := Validate(cfg)
		if err == nil || !strings.Contains(err.Error(), "caller-aware") {
			t.Fatalf("caller-aware rtd-once must be rejected, got %v", err)
		}
	})

	// Mode is accepted by the mode switch (case-insensitive).
	t.Run("mode case-insensitive", func(t *testing.T) {
		cfg := mk(Function{Name: "Compute", Mode: "RTD-ONCE", Return: "float"})
		if err := Validate(cfg); err != nil {
			t.Fatalf("RTD-ONCE (uppercase) must validate, got %v", err)
		}
	})

	// memoize_ttl: the middle ground. Accepted on rtd-once with a positive
	// duration; rejected on other modes; mutually exclusive with memoize:true;
	// must parse to a positive duration.
	t.Run("memoize_ttl on rtd-once ok", func(t *testing.T) {
		for _, ttl := range []string{"30s", "5m", "1500ms", "1h"} {
			cfg := mk(Function{Name: "Compute", Mode: "rtd-once", Return: "float", MemoizeTTL: ttl})
			if err := Validate(cfg); err != nil {
				t.Fatalf("memoize_ttl %q on rtd-once must be valid, got %v", ttl, err)
			}
		}
	})
	for _, mode := range []string{"sync", "async", "rtd"} {
		t.Run("memoize_ttl rejected on "+mode, func(t *testing.T) {
			cfg := &Config{
				Project:   ProjectConfig{Name: "TestProject"},
				Rtd:       RtdConfig{Enabled: true, ProgID: "P.Rtd"},
				Functions: []Function{{Name: "F", Mode: mode, Return: "int", MemoizeTTL: "30s"}},
			}
			err := Validate(cfg)
			if err == nil || !strings.Contains(err.Error(), "memoize_ttl is only valid with mode:\"rtd-once\"") {
				t.Fatalf("memoize_ttl on %q must be rejected with the memoize_ttl message, got %v", mode, err)
			}
		})
	}
	t.Run("memoize_ttl + memoize:true mutually exclusive", func(t *testing.T) {
		cfg := mk(Function{Name: "Compute", Mode: "rtd-once", Return: "float", Memoize: true, MemoizeTTL: "30s"})
		err := Validate(cfg)
		if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("memoize_ttl + memoize:true must be rejected as mutually exclusive, got %v", err)
		}
	})
	t.Run("memoize_ttl unparseable rejected", func(t *testing.T) {
		cfg := mk(Function{Name: "Compute", Mode: "rtd-once", Return: "float", MemoizeTTL: "notaduration"})
		err := Validate(cfg)
		if err == nil || !strings.Contains(err.Error(), "memoize_ttl") {
			t.Fatalf("unparseable memoize_ttl must be rejected, got %v", err)
		}
	})
	for _, ttl := range []string{"0s", "0", "-5s"} {
		t.Run("memoize_ttl non-positive rejected "+ttl, func(t *testing.T) {
			cfg := mk(Function{Name: "Compute", Mode: "rtd-once", Return: "float", MemoizeTTL: ttl})
			err := Validate(cfg)
			if err == nil || !strings.Contains(err.Error(), "positive duration") {
				t.Fatalf("non-positive memoize_ttl %q must be rejected, got %v", ttl, err)
			}
		})
	}

	// loading_placeholder: per-function value accepted on the RTD-backed modes
	// (rtd, rtd-once) for any string, rejected on the non-RTD modes. The global
	// rtd.loading_placeholder is never validated here.
	for _, mode := range []string{"rtd", "rtd-once"} {
		t.Run("loading_placeholder on "+mode+" ok", func(t *testing.T) {
			for _, ph := range []string{"getting_data", "na", "Loading...", "잠시만요"} {
				cfg := mk(Function{Name: "Compute", Mode: mode, Return: "float", LoadingPlaceholder: ph})
				if err := Validate(cfg); err != nil {
					t.Fatalf("loading_placeholder %q on %s must be valid, got %v", ph, mode, err)
				}
			}
		})
	}
	for _, mode := range []string{"sync", "async"} {
		t.Run("loading_placeholder rejected on "+mode, func(t *testing.T) {
			cfg := &Config{
				Project:   ProjectConfig{Name: "TestProject"},
				Rtd:       RtdConfig{Enabled: true, ProgID: "P.Rtd"},
				Functions: []Function{{Name: "F", Mode: mode, Return: "int", LoadingPlaceholder: "na"}},
			}
			err := Validate(cfg)
			if err == nil || !strings.Contains(err.Error(), "loading_placeholder is only valid with mode:\"rtd\" or mode:\"rtd-once\"") {
				t.Fatalf("loading_placeholder on %q must be rejected with the placeholder message, got %v", mode, err)
			}
		})
	}
	t.Run("global loading_placeholder needs no rtd-once function", func(t *testing.T) {
		cfg := &Config{
			Project:   ProjectConfig{Name: "TestProject"},
			Rtd:       RtdConfig{Enabled: true, ProgID: "P.Rtd", LoadingPlaceholder: "na"},
			Functions: []Function{{Name: "F", Mode: "sync", Return: "int"}},
		}
		if err := Validate(cfg); err != nil {
			t.Fatalf("global rtd.loading_placeholder must be a harmless no-op without rtd-once functions, got %v", err)
		}
	})
}

// TestResolveRtdPlaceholder pins the precedence (per-function over global over
// the getting_data default) and the keyword-vs-verbatim classification.
func TestResolveRtdPlaceholder(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		fnPH     string
		globalPH string
		wantKind RtdPlaceholderKind
		wantText string
	}{
		{"both empty -> default getting_data", "", "", PlaceholderGettingData, ""},
		{"global na inherited", "", "na", PlaceholderNA, ""},
		{"global text inherited", "", "Loading...", PlaceholderText, "Loading..."},
		{"per-fn overrides global", "getting_data", "na", PlaceholderGettingData, ""},
		{"per-fn na overrides global text", "na", "Hold on", PlaceholderNA, ""},
		{"keyword case-insensitive", "Getting_Data", "", PlaceholderGettingData, ""},
		{"na case-insensitive", "NA", "", PlaceholderNA, ""},
		{"whitespace trimmed then text", "  Working  ", "", PlaceholderText, "Working"},
		{"verbatim text wins over global", "Custom", "na", PlaceholderText, "Custom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveRtdPlaceholder(
				Function{LoadingPlaceholder: tc.fnPH},
				RtdConfig{LoadingPlaceholder: tc.globalPH},
			)
			if got.Kind != tc.wantKind {
				t.Errorf("Kind = %v, want %v", got.Kind, tc.wantKind)
			}
			if got.Kind == PlaceholderText && got.Text != tc.wantText {
				t.Errorf("Text = %q, want %q", got.Text, tc.wantText)
			}
		})
	}
}

// TestValidate_CallerMacroSplit pins the v0.5.0 caller/macro split rules:
//   - caller:true alone is accepted on every mode it was accepted before
//     (sync/async/rtd), and still rejected on rtd-once.
//   - macro:true mirrors caller's mode rules exactly: accepted on
//     sync/async/rtd, rejected on rtd-once.
//   - caller and macro are independent flags and combine freely on the allowed
//     modes.
func TestValidate_CallerMacroSplit(t *testing.T) {
	mk := func(fn Function) *Config {
		return &Config{
			Project:   ProjectConfig{Name: "TestProject"},
			Rtd:       RtdConfig{Enabled: true, ProgID: "P.Rtd"},
			Functions: []Function{fn},
		}
	}

	// caller:true alone accepted on sync/async/rtd (unchanged from pre-split).
	for _, mode := range []string{"sync", "async", "rtd"} {
		t.Run("caller accepted on "+mode, func(t *testing.T) {
			cfg := mk(Function{Name: "F", Mode: mode, Return: "string", Caller: true})
			if err := Validate(cfg); err != nil {
				t.Fatalf("caller:true on %q must be accepted, got %v", mode, err)
			}
		})
	}

	// macro:true alone accepted on the same modes caller is accepted on.
	for _, mode := range []string{"sync", "async", "rtd"} {
		t.Run("macro accepted on "+mode, func(t *testing.T) {
			cfg := mk(Function{Name: "F", Mode: mode, Return: "string", Macro: true})
			if err := Validate(cfg); err != nil {
				t.Fatalf("macro:true on %q must be accepted, got %v", mode, err)
			}
		})
	}

	// caller+macro combine freely on an allowed mode.
	t.Run("caller+macro accepted on sync", func(t *testing.T) {
		cfg := mk(Function{Name: "F", Mode: "sync", Return: "string", Caller: true, Macro: true})
		if err := Validate(cfg); err != nil {
			t.Fatalf("caller+macro on sync must be accepted, got %v", err)
		}
	})

	// macro:true rejected on rtd-once, mirroring caller's rejection there.
	t.Run("macro rejected on rtd-once", func(t *testing.T) {
		cfg := mk(Function{Name: "F", Mode: "rtd-once", Return: "float", Macro: true})
		err := Validate(cfg)
		if err == nil || !strings.Contains(err.Error(), "macro:true") {
			t.Fatalf("macro:true on rtd-once must be rejected with the macro message, got %v", err)
		}
	})

	// caller:true still rejected on rtd-once (pre-split rule preserved).
	t.Run("caller rejected on rtd-once", func(t *testing.T) {
		cfg := mk(Function{Name: "F", Mode: "rtd-once", Return: "float", Caller: true})
		err := Validate(cfg)
		if err == nil || !strings.Contains(err.Error(), "caller-aware") {
			t.Fatalf("caller:true on rtd-once must be rejected, got %v", err)
		}
	})
}

// TestValidate_RtdOnce_AllowsGridReturn pins that grid/numgrid are now valid
// rtd-once return types (they spill via the RtdOnceGridRegistry path; see the
// rtd-once-grid-spill design).
func TestValidate_RtdOnce_AllowsGridReturn(t *testing.T) {
	for _, ret := range []string{"grid", "numgrid"} {
		cfg := &Config{
			Project: ProjectConfig{Name: "TestProject"},
			Rtd:     RtdConfig{Enabled: true, ProgID: "P.Rtd"},
			Functions: []Function{{
				Name:   "BDH",
				Mode:   "rtd-once",
				Return: ret,
				Args:   []Arg{{Name: "t", Type: "string"}},
			}},
		}
		if err := Validate(cfg); err != nil {
			t.Fatalf("rtd-once return %q must be allowed, got: %v", ret, err)
		}
	}
}

// TestValidate_RtdOnce_StillRejectsRangeReturn pins that "range" remains
// unsupported as a return type even after grid/numgrid were allowed.
func TestValidate_RtdOnce_StillRejectsRangeReturn(t *testing.T) {
	cfg := &Config{
		Project: ProjectConfig{Name: "TestProject"},
		Rtd:     RtdConfig{Enabled: true, ProgID: "P.Rtd"},
		Functions: []Function{{
			Name:   "Bad",
			Mode:   "rtd-once",
			Return: "range",
		}},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("rtd-once return \"range\" must still be rejected")
	}
}

// TestValidate_Identifiers pins Defect D: function names, argument names, and
// handler names are validated as legal generated Go/C++ identifiers.
func TestValidate_Identifiers(t *testing.T) {
	fnCfg := func(name string, args []Arg) *Config {
		return &Config{
			Project:   ProjectConfig{Name: "TestProject"},
			Functions: []Function{{Name: name, Return: "int", Args: args}},
		}
	}

	// Function name: bad charset, leading digit, C++/Go keyword.
	for _, bad := range []string{"my func", "my-func", "2fast", "int", "new", "type"} {
		if err := Validate(fnCfg(bad, nil)); err == nil {
			t.Errorf("function name %q must be rejected", bad)
		}
	}
	// Valid function names (incl. underscore, which is fine for a function).
	for _, ok := range []string{"MyFunc", "compute_v2", "HandleData"} {
		if err := Validate(fnCfg(ok, nil)); err != nil {
			t.Errorf("function name %q must be valid, got %v", ok, err)
		}
	}

	// Duplicate function names.
	dup := &Config{
		Project: ProjectConfig{Name: "TestProject"},
		Functions: []Function{
			{Name: "Foo", Return: "int"},
			{Name: "Foo", Return: "int"},
		},
	}
	if err := Validate(dup); err == nil || !strings.Contains(err.Error(), "duplicate function") {
		t.Errorf("duplicate function name must be rejected, got %v", err)
	}

	// Argument names: keyword, bad charset, leading digit, and — crucially —
	// underscores (flatc camelizes the accessor, breaking the generated Go).
	for _, bad := range []string{"type", "new", "class", "1st", "bad name", "start_date"} {
		if err := Validate(fnCfg("Fn", []Arg{{Name: bad, Type: "int"}})); err == nil {
			t.Errorf("argument name %q must be rejected", bad)
		}
	}
	// Valid arg names (lowerCamelCase, no underscore).
	if err := Validate(fnCfg("Fn", []Arg{{Name: "startDate", Type: "date"}})); err != nil {
		t.Errorf("arg startDate must be valid, got %v", err)
	}
	// Duplicate argument names within a function.
	if err := Validate(fnCfg("Fn", []Arg{{Name: "x", Type: "int"}, {Name: "x", Type: "int"}})); err == nil ||
		!strings.Contains(err.Error(), "duplicate argument") {
		t.Errorf("duplicate arg name must be rejected")
	}

	// Command handler and event handler identifier checks.
	cmdBad := &Config{
		Project:  ProjectConfig{Name: "TestProject"},
		Commands: []Command{{Name: "DoIt", Handler: "func"}},
	}
	if err := Validate(cmdBad); err == nil || !strings.Contains(err.Error(), "reserved word") {
		t.Errorf("command handler that is a Go keyword must be rejected, got %v", err)
	}
	evtBad := &Config{
		Project: ProjectConfig{Name: "TestProject"},
		Events:  []Event{{Type: "CalculationEnded", Handler: "2bad"}},
	}
	if err := Validate(evtBad); err == nil {
		t.Errorf("event handler with a leading digit must be rejected")
	}
}
