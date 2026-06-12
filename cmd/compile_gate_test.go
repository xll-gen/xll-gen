package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/xll-gen/xll-gen/internal/generator"
)

// compileGateYaml exercises the full type-sensitive codegen surface so that
// `go build ./...` of the generated project actually type-checks every branch
// the templates emit. The parse-only gate (regression_static_test.go) cannot
// catch type errors — e.g. the historical composite-return breakage and the
// v0.5.0 spill codegen, which until now was only compile-checked by hand via
// the showcase. One project covers:
//
//   - sync scalar (int)            -> direct AddResult(int) branch
//   - sync any return              -> BuildAnyFromGo / fbany path
//   - sync grid return             -> server.BuildGridFromGo ([][]any)
//   - sync numgrid return          -> server.BuildNumGridFromGo ([][]float64)
//   - async scalar (int)           -> QueueResult(AnyValueInt)
//   - async grid                   -> QueueResult(AnyValueGrid) (the 0xc0000374 fix)
//   - rtd scalar                   -> Name_RTD signature + dispatch skip
//   - rtd range arg                -> content-hash payload path (server.ResolveRangeArg)
//   - rtd-once + memoize_ttl        -> rtd.RunOnce wrapping, normal handler shape
//   - rtd-once grid arg            -> content-hash payload path (server.ResolveGridArg)
//   - caller-only                  -> position-only xlfCaller, caller *protocol.Range
//   - caller + macro               -> macro-sheet ('#') + xlfGetCell number format
//   - a command + structured ribbon -> CommandContext handler, ribbon XML emit
//   - grid/range/any/scalar args    -> lookupGoType arg view types
const compileGateYaml = `project:
  name: "compile_gate"
  version: "0.1.0"

events:
  - type: CalculationEnded
    handler: OnCalcEnded

rtd:
  enabled: true
  prog_id: "CompileGate.RTD"

commands:
  - name: "RunReport"
    handler: "RunReport"
    shortcut: "R"

ribbon:
  tab: "Gate"
  groups:
    - label: "Tools"
      buttons:
        - label: "Run"
          command: "RunReport"
          image: "HappyFace"

gen:
  go:
    package: "generated"

logging:
  level: "debug"

functions:
  # sync scalar
  - name: "SyncScalar"
    args: [{name: "v", type: "int"}]
    return: "int"

  # sync any return + any arg
  - name: "SyncAny"
    args: [{name: "v", type: "any"}]
    return: "any"

  # sync grid return + grid arg
  - name: "SyncGrid"
    args: [{name: "g", type: "grid"}]
    return: "grid"

  # sync numgrid return + numgrid arg
  - name: "SyncNumGrid"
    args: [{name: "g", type: "numgrid"}]
    return: "numgrid"

  # sync with range arg + scalar arg mix
  - name: "SyncRange"
    args: [{name: "r", type: "range"}, {name: "n", type: "float"}, {name: "s", type: "string"}, {name: "b", type: "bool"}]
    return: "string"

  # async scalar
  - name: "AsyncScalar"
    mode: "async"
    args: [{name: "v", type: "int"}]
    return: "int"

  # async grid (the heap-corruption fix path)
  - name: "AsyncGrid"
    mode: "async"
    args: [{name: "n", type: "int"}]
    return: "grid"

  # rtd
  - name: "RtdTick"
    mode: "rtd"
    args: [{name: "symbol", type: "string"}]
    return: "float"

  # rtd with a composite (range) arg — content-hash payload path
  - name: "RtdRangeSum"
    mode: "rtd"
    args: [{name: "r", type: "range"}]
    return: "float"

  # rtd-once with memoize_ttl
  - name: "RtdOnceTTL"
    mode: "rtd-once"
    memoize_ttl: "30s"
    args: [{name: "n", type: "int"}]
    return: "float"

  # rtd-once with a composite (grid) arg — content-hash payload path
  - name: "SumGridOnce"
    mode: "rtd-once"
    args: [{name: "g", type: "grid"}]
    return: "float"

  # caller-only (position)
  - name: "CallerOnly"
    caller: true
    args: [{name: "v", type: "int"}]
    return: "string"

  # caller + macro (number format)
  - name: "CallerMacro"
    caller: true
    macro: true
    args: [{name: "v", type: "int"}]
    return: "string"
`

// compileGateMain implements the generated XllService interface for the
// compileGateYaml fixture. It must stay in lockstep with the function/command/
// event/rtd set declared above (interface.go.tmpl shapes the signatures).
const compileGateMain = `package main

import (
	"context"

	"compile_gate/generated"

	protocol "github.com/xll-gen/types/go/protocol"
	"github.com/xll-gen/xll-gen/pkg/server"
)

type Service struct{}

func (s *Service) SyncScalar(ctx context.Context, v int32) (int32, error) { return v, nil }

func (s *Service) SyncAny(ctx context.Context, v *protocol.Any) (any, error) { return int32(1), nil }

func (s *Service) SyncGrid(ctx context.Context, g *protocol.Grid) ([][]any, error) {
	return [][]any{{int32(1), "x"}, {2.0, true}}, nil
}

func (s *Service) SyncNumGrid(ctx context.Context, g *protocol.NumGrid) ([][]float64, error) {
	return [][]float64{{1, 2}, {3, 4}}, nil
}

func (s *Service) SyncRange(ctx context.Context, r *protocol.Range, n float64, str string, b bool) (string, error) {
	return str, nil
}

func (s *Service) AsyncScalar(ctx context.Context, v int32) (int32, error) { return v, nil }

func (s *Service) AsyncGrid(ctx context.Context, n int32) ([][]any, error) {
	return [][]any{{int32(n)}}, nil
}

func (s *Service) RtdTick_RTD(ctx context.Context, topicID int32, symbol string) error { return nil }

func (s *Service) RtdRangeSum_RTD(ctx context.Context, topicID int32, r *protocol.Range) error {
	return nil
}

func (s *Service) RtdOnceTTL(ctx context.Context, n int32) (float64, error) { return float64(n), nil }

func (s *Service) SumGridOnce(ctx context.Context, g *protocol.Grid) (float64, error) {
	return float64(g.Rows() * g.Cols()), nil
}

func (s *Service) CallerOnly(ctx context.Context, v int32, caller *protocol.Range) (string, error) {
	return "", nil
}

func (s *Service) CallerMacro(ctx context.Context, v int32, caller *protocol.Range) (string, error) {
	return "", nil
}

func (s *Service) RunReport(ctx context.Context, cmd server.CommandContext) error { return nil }

func (s *Service) OnCalcEnded(ctx context.Context) error { return nil }

func (s *Service) OnCalculationCanceled(ctx context.Context) error { return nil }

func (s *Service) OnRtdConnect(ctx context.Context, topicID int32, strings []string, newValues bool) error {
	return nil
}

func (s *Service) OnRtdDisconnect(ctx context.Context, topicID int32) error { return nil }

func main() { generated.Serve(&Service{}) }
`

// repoRootForCompileGate returns the absolute path of the xll-gen repository
// working tree from the test binary's source location, so the generated
// project's go.mod can `replace github.com/xll-gen/xll-gen => <working tree>`.
// This is what makes the gate test the CURRENT code rather than a published
// tag. cmd/compile_gate_test.go lives one directory below the repo root.
func repoRootForCompileGate(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(filepath.Dir(thisFile)) // .../cmd -> repo root
}

// TestGeneratedServerCompiles is the "generated Go server actually compiles"
// regression gate. It generates a project covering the full type-sensitive
// codegen surface, points its go.mod at the working tree, runs go mod tidy,
// and `go build ./...`. Unlike the parse-only gates this catches type errors
// in the templates (composite returns, async grid serialization, caller
// signatures, rtd/rtd-once dispatch, command/ribbon wiring).
//
// It uses the REAL flatc (generator.Options{} -> flatc.EnsureFlatc, cached
// download) because the generated server.go imports generated/ipc/*.go, whose
// per-message FlatBuffers types (e.g. ipc.SyncScalarRequest,
// ipc.GetRootAsSyncScalarRequest) are produced by flatc from the project
// schema — a MOCK flatc emits placeholder stubs without those symbols, so the
// Go build would not compile. The cmd suite already runs real init+generate in
// temp dirs (TestGenerate ~50s) and TestRegression already does the same
// replace+tidy+build dance, so this fits the existing slow-test budget.
//
// Gated by testing.Short(), matching TestRegression / the smoke suite.
func TestGeneratedServerCompiles(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping generated-server compile gate in short mode")
	}

	repoRoot := repoRootForCompileGate(t)

	tempDir, err := os.MkdirTemp("", "xll-compile-gate")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	projectName := "compile_gate"
	projectDir := filepath.Join(tempDir, projectName)
	if err := runInit(projectDir, false, false); err != nil {
		t.Fatalf("runInit failed: %v", err)
	}

	// Point the generated module at the working tree, not the published tag,
	// so the gate tests CURRENT code (server.go.tmpl, pkg/server, etc.).
	editCmd := exec.Command("go", "mod", "edit",
		"-replace", "github.com/xll-gen/xll-gen="+repoRoot)
	editCmd.Dir = projectDir
	if out, err := editCmd.CombinedOutput(); err != nil {
		t.Fatalf("go mod edit replace failed: %v\n%s", err, out)
	}

	// Overwrite the scaffolded config + handler with the full-surface fixture.
	if err := os.WriteFile(filepath.Join(projectDir, "xll.yaml"), []byte(compileGateYaml), 0644); err != nil {
		t.Fatal(err)
	}

	// generator.Options{} -> real, cached flatc (see doc comment above).
	runGenerateInDir(t, projectDir, generator.Options{})

	if err := os.WriteFile(filepath.Join(projectDir, "main.go"), []byte(compileGateMain), 0644); err != nil {
		t.Fatal(err)
	}

	tidyCmd := exec.Command("go", "mod", "tidy")
	tidyCmd.Dir = projectDir
	if out, err := tidyCmd.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy failed: %v\n%s", err, out)
	}

	buildCmd := exec.Command("go", "build", "./...")
	buildCmd.Dir = projectDir
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("generated project failed to compile (this is the regression the gate exists to catch):\n%s", out)
	}
}
