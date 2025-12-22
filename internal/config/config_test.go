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
