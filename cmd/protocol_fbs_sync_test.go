package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/templates"
)

// TestProtocolFbsMatchesPinnedTypes is the single-source drift gate for the
// FlatBuffers protocol schema.
//
// `types/go/protocol/protocol.fbs` is the schema's source of truth. xll-gen
// embeds a verbatim copy at internal/templates/protocol.fbs, written into every
// generated project as a flatc parse-stub for schema.fbs's `include
// "protocol.fbs"`. The generated project's actual protocol code (Go and C++)
// comes from the pinned `types` module — never from this copy — so the copy is
// load-bearing only insofar as it must let schema.fbs parse against the same
// type universe the build links. If it drifts from the pinned types schema,
// flatc can fail to resolve a referenced type (or silently parse against a
// stale shape).
//
// This test fails when the embedded copy diverges from the protocol.fbs in the
// `types` version this module pins. Fix a failure with:
//
//	go generate ./internal/templates/
//
// Comparison is line-ending-insensitive: the embedded copy is subject to the
// checkout's autocrlf (it may be CRLF on Windows working trees), while the
// module-cache copy is always LF, and flatc accepts either — so a CRLF/LF skew
// is not real drift. Only the schema content matters.
func TestProtocolFbsMatchesPinnedTypes(t *testing.T) {
	// Resolve the directory of the pinned types module (honors replace), i.e.
	// the exact schema generated projects link against.
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/xll-gen/types").Output()
	if err != nil {
		t.Fatalf("resolve types module dir: %v", err)
	}
	src := filepath.Join(strings.TrimSpace(string(out)), "go", "protocol", "protocol.fbs")

	wantRaw, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read pinned types protocol.fbs (%s): %v", src, err)
	}
	want := normalizeFbs(string(wantRaw))

	gotRaw, err := templates.Get("protocol.fbs")
	if err != nil {
		t.Fatalf("read embedded template protocol.fbs: %v", err)
	}
	got := normalizeFbs(gotRaw)

	if got != want {
		t.Fatalf("internal/templates/protocol.fbs drifted from the pinned types module (%s).\n"+
			"Re-sync with:\n\n    go generate ./internal/templates/\n\n"+
			"types is the schema source of truth; the embedded copy must stay byte-identical to the pinned version.", src)
	}
}

// normalizeFbs strips CR so the comparison ignores CRLF/LF differences, and
// trims trailing blank lines.
func normalizeFbs(s string) string {
	return strings.TrimRight(strings.ReplaceAll(s, "\r", ""), "\n")
}
