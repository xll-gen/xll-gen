package config

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Load reads and parses an xll.yaml file at path. It is the single strict
// parse path shared by `xll-gen generate` and `xll-gen init`.
//
// The returned Config is NOT defaulted or validated — callers run
// ApplyDefaults + Validate as before.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read xll.yaml: %w", err)
	}
	return Parse(data)
}

// Parse decodes xll.yaml bytes into a Config with strict unknown-key detection
// (yaml.Decoder.KnownFields). A misspelled or unsupported key (e.g. `retrun:`)
// fails here with the yaml.v3 error — which includes line information — instead
// of being silently ignored, so configuration typos surface immediately.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to parse xll.yaml: %w", err)
	}
	return &cfg, nil
}
