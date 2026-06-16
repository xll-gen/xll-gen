// Command syncprotocol copies protocol.fbs from the pinned `types` module into
// internal/templates/protocol.fbs so the FlatBuffers schema stays single-sourced.
//
// `types/go/protocol/protocol.fbs` is the schema's single source of truth (see
// types/AGENTS.md). xll-gen embeds a verbatim copy only as a flatc parse-stub
// for the generated project's schema.fbs `include "protocol.fbs"` — both the Go
// and C++ protocol code in generated projects come from the pinned `types`
// module, not from this copy. Keeping the copy byte-identical to the pinned
// version is enforced in CI by cmd.TestProtocolFbsMatchesPinnedTypes; this tool
// is how you fix a reported drift.
//
// Run from the repo (or anywhere in the module):
//
//	go generate ./internal/templates/
package main

import (
	"bytes"
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

func main() {
	log.SetFlags(0)

	// Resolve the directory of the `types` version this module pins. This honors
	// any replace directive, so the synced copy always matches what generated
	// projects actually link against.
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/xll-gen/types").Output()
	if err != nil {
		log.Fatalf("resolve types module dir: %v", err)
	}
	src := filepath.Join(string(bytes.TrimSpace(out)), "go", "protocol", "protocol.fbs")

	data, err := os.ReadFile(src)
	if err != nil {
		log.Fatalf("read %s: %v", src, err)
	}

	// go generate runs with the working directory set to the package that holds
	// the //go:generate directive (internal/templates), so a bare filename writes
	// the embedded copy in place. Module-cache files are read-only (0444); write
	// 0644 so the repo copy stays editable by the sync.
	const dst = "protocol.fbs"
	if err := os.WriteFile(dst, data, 0644); err != nil {
		log.Fatalf("write %s: %v", dst, err)
	}
	log.Printf("synced %s from %s (%d bytes)", dst, src, len(data))
}
