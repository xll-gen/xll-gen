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

// TestValidate_CompositeReturnTypes locks in that composite table types
// (range/grid/numgrid) are rejected as RETURN types — the generated Go
// server cannot serialize them as returns (sync: compile error, async: dropped
// result) — while remaining valid as ARGUMENT types. Scalar returns
// (int/float/string/bool) and "any" (serialized via pkg/server.BuildAnyFromGo)
// stay valid.
func TestValidate_CompositeReturnTypes(t *testing.T) {
	composite := []string{"range", "grid", "numgrid", "any"}
	rejected := []string{"range", "grid", "numgrid"}

	// Each composite table type must be REJECTED as a return type, with a
	// message explaining it's arg-only.
	for _, typ := range rejected {
		t.Run("reject "+typ+" return", func(t *testing.T) {
			cfg := &Config{
				Project:   ProjectConfig{Name: "TestProject"},
				Functions: []Function{{Name: "TestFunc", Return: typ}},
			}
			err := Validate(cfg)
			if err == nil {
				t.Fatalf("Validate() expected error for return type %q, got nil", typ)
			}
			msg := err.Error()
			if !strings.Contains(msg, "argument type but not yet as a return type") {
				t.Errorf("Validate() error = %v, want explanation about arg-only support", err)
			}
			if !strings.Contains(msg, typ) {
				t.Errorf("Validate() error = %v, want mention of type %q", err, typ)
			}
		})
	}

	// Each composite/any type must STILL be ACCEPTED as a RETURN type for RTD
	// functions: RTD streams results through pkg/rtd, not the sync/async
	// server serialization that breaks on composite returns. The default
	// scaffold ships StockQuote (mode:"rtd", return:"any").
	for _, typ := range composite {
		t.Run("allow "+typ+" return for rtd", func(t *testing.T) {
			cfg := &Config{
				Project: ProjectConfig{Name: "TestProject"},
				Functions: []Function{
					{Name: "TestFunc", Mode: "rtd", Return: typ},
				},
			}
			if err := Validate(cfg); err != nil {
				t.Errorf("Validate() unexpected error for rtd return %q: %v", typ, err)
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
