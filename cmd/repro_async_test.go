package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/generator"
)

func TestRepro_MultipleAsync(t *testing.T) {
	t.Parallel()
	projectDir, cleanup := setupGenTest(t, "repro_async")
	defer cleanup()

	flatcPath, pathCleanup := setupMockFlatc(t, filepath.Dir(projectDir))
	defer pathCleanup()

	xllContent := `project:
  name: "repro-async"
  version: "0.1.0"
gen:
  go:
    package: "generated"
functions:
  - name: "AsyncOne"
    args: [{name: "a", type: "int"}]
    return: "int"
    async: true
  - name: "AsyncTwo"
    args: [{name: "b", type: "int"}]
    return: "int"
    async: true
`
	if err := os.WriteFile(filepath.Join(projectDir, "xll.yaml"), []byte(xllContent), 0644); err != nil {
		t.Fatal(err)
	}
	// Dummy go.mod
	if err := os.WriteFile(filepath.Join(projectDir, "go.mod"), []byte("module repro-async\n"), 0644); err != nil {
		t.Fatal(err)
	}

	runGenerateInDir(t, projectDir, generator.Options{FlatcPath: flatcPath})

	content, err := os.ReadFile(filepath.Join(projectDir, "generated", "server.go"))
	if err != nil {
		t.Fatal(err)
	}
	sContent := string(content)

	// The original assertion checked for a `queueAsyncResult` helper that
	// the server.go template declared once and called from each async func
	// site. That helper was refactored away when async batching moved
	// behind `asyncBatcher.QueueResult` (now invoked at each call site
	// directly). The new invariant: every async function in xll.yaml gets
	// its own `asyncBatcher.QueueResult` call wired up — at least one per
	// function, no fewer. With 2 async functions declared above we expect
	// at least 2 calls.
	const want = 2
	got := strings.Count(sContent, "asyncBatcher.QueueResult(")
	if got < want {
		t.Errorf("asyncBatcher.QueueResult invoked %d times, want >= %d", got, want)
	}
}
