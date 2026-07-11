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
