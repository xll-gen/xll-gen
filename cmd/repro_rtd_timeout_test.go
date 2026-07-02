package cmd

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/xll-gen/xll-gen/internal/config"
)

// TestGen_RtdTimeoutRejected is the generate front-door fixture for Defect B:
// an rtd(-once) function carrying a per-function timeout must be rejected by the
// ApplyDefaults+Validate gate that `xll-gen generate` runs (see
// runGenerateInDir / cmd/generate.go) BEFORE any template renders. Without the
// reject, generation would emit a server.go that declares timeout_<Name> for the
// rtd function but never uses it (the dispatch case is non-rtd only) →
// `go build` fails with "declared and not used".
func TestGen_RtdTimeoutRejected(t *testing.T) {
	t.Parallel()
	const rtdTimeoutYaml = `project:
  name: "rtd_timeout_repro"
  version: "0.1.0"
gen:
  go:
    package: "generated"
rtd:
  enabled: true
  prog_id: "Repro.Rtd"
functions:
  - name: "SlowTick"
    mode: "rtd"
    args: [{name: "symbol", type: "string"}]
    return: "float"
    timeout: "2s"
`
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(rtdTimeoutYaml), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	config.ApplyDefaults(&cfg)
	err := config.Validate(&cfg)
	if err == nil || !strings.Contains(err.Error(), "timeout is not supported") {
		t.Fatalf("rtd + timeout must be rejected at the generate gate, got %v", err)
	}
}
