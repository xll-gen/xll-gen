package config

import (
	"strings"
	"testing"
)

// TestParse_KnownGoodConfig verifies a config exercising the fields the scaffold
// ships (including the recognized-but-not-yet-wired gen.go.package and
// server.launch.command/cwd) parses cleanly under strict unknown-key detection.
func TestParse_KnownGoodConfig(t *testing.T) {
	const yaml = `
project:
  name: "demo"
  version: "0.1.0"
gen:
  go:
    package: "generated"
  disable_pid_suffix: true
logging:
  level: "info"
  dir: "${BIN_DIR}"
server:
  timeout: "10s"
  workers: 0
  launch:
    enabled: true
    command: "${BIN}"
    cwd: "${BIN_DIR}"
functions:
  - name: "Add"
    args:
      - name: "a"
        type: "int"
    return: "int"
`
	cfg, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse of a known-good config failed: %v", err)
	}
	if cfg.Project.Name != "demo" {
		t.Errorf("Project.Name = %q, want demo", cfg.Project.Name)
	}
	if cfg.Gen.Go.Package != "generated" {
		t.Errorf("Gen.Go.Package = %q, want generated", cfg.Gen.Go.Package)
	}
	if cfg.Server.Launch == nil || cfg.Server.Launch.Command != "${BIN}" {
		t.Errorf("Server.Launch.Command not parsed: %+v", cfg.Server.Launch)
	}
}

// TestParse_UnknownKeyRejected is the core regression for the unknown-key
// detection: a misspelled top-level key (`retrun` instead of a real field) and a
// misspelled nested key must both fail, with the offending name in the error.
func TestParse_UnknownKeyRejected(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantSub string
	}{
		{
			name: "misspelled function return",
			yaml: `
project:
  name: "demo"
functions:
  - name: "Add"
    retrun: "int"
`,
			wantSub: "retrun",
		},
		{
			name: "unknown top-level key",
			yaml: `
project:
  name: "demo"
loging:
  level: "info"
`,
			wantSub: "loging",
		},
		{
			name: "unknown nested server key",
			yaml: `
project:
  name: "demo"
server:
  timeoutt: "10s"
`,
			wantSub: "timeoutt",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.yaml))
			if err == nil {
				t.Fatalf("Parse accepted config with unknown key %q", c.wantSub)
			}
			if !strings.Contains(err.Error(), c.wantSub) {
				t.Errorf("error %q does not mention the offending key %q", err, c.wantSub)
			}
		})
	}
}

// TestParse_ServerChunk covers the server.chunk tuning block end to end: strict
// unknown-key detection, field decoding, and Validate's bounds. Added with
// max_concurrent_transfers (the aggregate in-flight-transfer bound); the
// existing three knobs ride along so the block as a whole is pinned.
func TestParse_ServerChunk(t *testing.T) {
	t.Run("AllKnobsParseAndValidate", func(t *testing.T) {
		const yaml = `
project:
  name: "demo"
server:
  chunk:
    max_buffer_bytes: 134217728
    cleanup_interval: "15s"
    buffer_ttl: "45s"
    max_concurrent_transfers: 512
functions:
  - name: "Add"
    args:
      - name: "a"
        type: "int"
    return: "int"
`
		cfg, err := Parse([]byte(yaml))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		c := cfg.Server.Chunk
		if c == nil {
			t.Fatal("Server.Chunk not decoded")
		}
		if c.MaxBufferBytes != 134217728 || c.CleanupInterval != "15s" || c.BufferTTL != "45s" || c.MaxConcurrentTransfers != 512 {
			t.Fatalf("Server.Chunk decoded wrong: %+v", c)
		}
		ApplyDefaults(cfg)
		if err := Validate(cfg); err != nil {
			t.Fatalf("Validate on a valid chunk block: %v", err)
		}
	})

	t.Run("NegativeMaxConcurrentTransfersRejected", func(t *testing.T) {
		const yaml = `
project:
  name: "demo"
server:
  chunk:
    max_concurrent_transfers: -1
functions:
  - name: "Add"
    args:
      - name: "a"
        type: "int"
    return: "int"
`
		cfg, err := Parse([]byte(yaml))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		ApplyDefaults(cfg)
		err = Validate(cfg)
		if err == nil {
			t.Fatal("Validate accepted a negative max_concurrent_transfers")
		}
		if !strings.Contains(err.Error(), "max_concurrent_transfers") {
			t.Fatalf("error %q does not name the offending key", err)
		}
	})

	t.Run("MisspelledChunkKeyRejected", func(t *testing.T) {
		const yaml = `
project:
  name: "demo"
server:
  chunk:
    max_concurrent_transfer: 512
`
		if _, err := Parse([]byte(yaml)); err == nil {
			t.Fatal("strict decoding accepted a misspelled chunk key")
		} else if !strings.Contains(err.Error(), "max_concurrent_transfer") {
			t.Fatalf("error %q does not mention the offending key", err)
		}
	})
}
