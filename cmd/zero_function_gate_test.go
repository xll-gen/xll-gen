package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/xll-gen/xll-gen/internal/generator"
)

// zeroFunctionGateYaml is a ribbon+command-ONLY project with ZERO worksheet
// functions. This is a fully supported shape (validateRibbon requires only
// `commands`), but it used to generate a server.go that could not compile:
// the ipc import was emitted unconditionally, yet with no functions flatc emits
// no ipc message tables (the schema is a bare namespace), so the ipc package
// either does not exist or has no referenced symbols -> `go build` failed with
// an unused/broken import. The fix gates the ipc import on {{if .Functions}}
// (server.go.tmpl); this gate pins it.
const zeroFunctionGateYaml = `project:
  name: "zero_fn_gate"
  version: "0.1.0"

commands:
  - name: "RunReport"
    handler: "RunReport"
    shortcut: "R"

ribbon:
  tab: "Tools"
  groups:
    - label: "Actions"
      buttons:
        - label: "Run"
          command: "RunReport"

gen:
  go:
    package: "generated"

logging:
  level: "debug"
`

// zeroFunctionGateMain implements the generated XllService for the
// ribbon+command-only fixture: one command handler plus the two mandatory
// calculation-event handlers (no functions, no RTD).
const zeroFunctionGateMain = `package main

import (
	"context"

	"zero_fn_gate/generated"

	"github.com/xll-gen/xll-gen/pkg/server"
)

type Service struct{}

func (s *Service) RunReport(ctx context.Context, cmd server.CommandContext) error { return nil }

func (s *Service) OnCalculationEnded(ctx context.Context) error { return nil }

func (s *Service) OnCalculationCanceled(ctx context.Context) error { return nil }

func main() { generated.Serve(&Service{}) }
`

// TestGeneratedServerCompiles_ZeroFunctions is the regression gate for the
// "function-count 0 (ribbon/command-only) config generates uncompilable output"
// defect. It generates the ribbon+command-only project, points its go.mod at the
// working tree, and runs `go mod tidy` + `go build ./...`. Before the ipc-import
// gate fix the build failed on the unconditional `{{.ModName}}/{{.Package}}/ipc`
// import. Mirrors TestGeneratedServerCompiles (real cached flatc; -short gated).
func TestGeneratedServerCompiles_ZeroFunctions(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("Skipping zero-function compile gate in short mode")
	}

	repoRoot := repoRootForCompileGate(t)

	tempDir, err := os.MkdirTemp("", "xll-zero-fn-gate")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	projectName := "zero_fn_gate"
	projectDir := filepath.Join(tempDir, projectName)
	if err := runInit(projectDir, false, false); err != nil {
		t.Fatalf("runInit failed: %v", err)
	}

	editCmd := exec.Command("go", "mod", "edit",
		"-replace", "github.com/xll-gen/xll-gen="+repoRoot)
	editCmd.Dir = projectDir
	if out, err := editCmd.CombinedOutput(); err != nil {
		t.Fatalf("go mod edit replace failed: %v\n%s", err, out)
	}

	// pkg/server references types symbols added after the pinned tag; when a
	// local types checkout is provided, replace the module (mirrors
	// TestGeneratedServerCompiles).
	if typesSrc := os.Getenv("XLLGEN_TYPES_SRC"); typesSrc != "" {
		typesEdit := exec.Command("go", "mod", "edit",
			"-replace", "github.com/xll-gen/types="+typesSrc)
		typesEdit.Dir = projectDir
		if out, err := typesEdit.CombinedOutput(); err != nil {
			t.Fatalf("go mod edit replace (types) failed: %v\n%s", err, out)
		}
	}

	if err := os.WriteFile(filepath.Join(projectDir, "xll.yaml"), []byte(zeroFunctionGateYaml), 0644); err != nil {
		t.Fatal(err)
	}

	// Real (cached) flatc: with zero functions the schema is a bare namespace,
	// so this reproduces the exact "no ipc symbols" condition the gate exists to
	// catch. A mock flatc would not.
	runGenerateInDir(t, projectDir, generator.Options{})

	if err := os.WriteFile(filepath.Join(projectDir, "main.go"), []byte(zeroFunctionGateMain), 0644); err != nil {
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
		t.Fatalf("zero-function project failed to compile (this is the regression the gate exists to catch):\n%s", out)
	}
}
