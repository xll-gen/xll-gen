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
			wantError: "type 'string?' is not supported",
		},
		{
			name:      "string? return",
			fnArgs:    []Arg{{Name: "a", Type: "int"}},
			fnReturn:  "string?",
			wantError: "type 'string?' is not supported",
		},
		{
			name:      "valid types",
			fnArgs:    []Arg{{Name: "a", Type: "string"}, {Name: "b", Type: "int?"}},
			fnReturn:  "bool",
			wantError: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
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
