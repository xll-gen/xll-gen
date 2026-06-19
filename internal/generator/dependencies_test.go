package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFixGoImportsRewritesGeneratedProtocolImport verifies that the protocol
// import flatc emits (alias + "<goModPath>/protocol") is rewritten to the
// pinned types-module path while the alias is preserved. This mirrors the real
// flatc output: `protocol "<modName>/generated/protocol"`.
func TestFixGoImportsRewritesGeneratedProtocolImport(t *testing.T) {
	goModPath := "temp_prj/generated"
	dir := t.TempDir()

	src := `package ipc

import (
	flatbuffers "github.com/google/flatbuffers/go"
	protocol "temp_prj/generated/protocol"
)

var _ = protocol.Bool{}
`
	file := filepath.Join(dir, "Resp.go")
	if err := os.WriteFile(file, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	if err := fixGoImports(dir, goModPath); err != nil {
		t.Fatalf("fixGoImports: %v", err)
	}

	got := readFile(t, file)
	const want = "github.com/xll-gen/types/go/protocol"
	if !strings.Contains(got, `protocol "`+want+`"`) {
		t.Errorf("expected rewritten import with preserved alias, got:\n%s", got)
	}
	if strings.Contains(got, `"temp_prj/generated/protocol"`) {
		t.Errorf("local protocol import was not rewritten:\n%s", got)
	}
	// The flatbuffers import must be untouched.
	if !strings.Contains(got, `flatbuffers "github.com/google/flatbuffers/go"`) {
		t.Errorf("flatbuffers import was altered:\n%s", got)
	}
}

// TestFixGoImportsLeavesUnrelatedImports guards the bug the anchored regex
// fixes: the previous "anything ending in /protocol" pattern would clobber
// third-party imports that merely end in /protocol.
func TestFixGoImportsLeavesUnrelatedImports(t *testing.T) {
	goModPath := "temp_prj/generated"
	dir := t.TempDir()

	src := `package ipc

import (
	"github.com/foo/protocol"
	other "github.com/bar/protocol"
	"github.com/foo/protocol/sub"
)
`
	file := filepath.Join(dir, "Unrelated.go")
	if err := os.WriteFile(file, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	if err := fixGoImports(dir, goModPath); err != nil {
		t.Fatalf("fixGoImports: %v", err)
	}

	got := readFile(t, file)
	if got != src {
		t.Errorf("unrelated imports were modified.\n--- want ---\n%s\n--- got ---\n%s", src, got)
	}
}

// TestFixGoImportsBareProtocol covers the legacy bare "protocol" form (no
// module-name prefix). The alias is absent, so none is forced on output.
func TestFixGoImportsBareProtocol(t *testing.T) {
	dir := t.TempDir()
	src := `package ipc

import (
	"protocol"
)
`
	file := filepath.Join(dir, "Bare.go")
	if err := os.WriteFile(file, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	if err := fixGoImports(dir, "temp_prj/generated"); err != nil {
		t.Fatalf("fixGoImports: %v", err)
	}

	got := readFile(t, file)
	if !strings.Contains(got, `"github.com/xll-gen/types/go/protocol"`) {
		t.Errorf("bare protocol import not rewritten:\n%s", got)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
