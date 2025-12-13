package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepro_MultipleAsync(t *testing.T) {
	tempDir, cleanup := setupGenTest(t, "repro_async")
	defer cleanup()

	pathCleanup := setupMockFlatc(t, tempDir)
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
	if err := os.WriteFile("xll.yaml", []byte(xllContent), 0644); err != nil {
		t.Fatal(err)
	}
	// Dummy go.mod
	if err := os.WriteFile("go.mod", []byte("module repro-async\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := runGenerate(); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join("generated", "server.go"))
	if err != nil {
		t.Fatal(err)
	}
	sContent := string(content)

	if c := strings.Count(sContent, "func queueAsyncResult"); c > 1 {
		t.Errorf("queueAsyncResult declared %d times", c)
	} else if c == 0 {
		t.Errorf("queueAsyncResult declared 0 times")
	}
}
