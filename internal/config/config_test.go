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
