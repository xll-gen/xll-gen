// Package fake-flatc is a minimal stand-in for the real flatc compiler used
// in cmd/ integration tests. It does just enough to keep generator.Generate
// happy on hosts that don't have flatc on PATH:
//
//   - flatc --version → prints "flatc version <pinned>"
//   - flatc --go --go-namespace <ns> [-o <dir>] <schema.fbs>
//       → writes "package <ns>\n" to <dir>/<base>_generated.go
//   - flatc --cpp [-o <dir>] <schema.fbs>
//       → writes a minimal header to <dir>/<base>_generated.h so the
//         generator's post-processing (fixCppImports) finds something
//         to rewrite.
//
// Anything else exits 0 silently. The stub is NOT a substitute for real
// flatc — tests that check flatc-generated code content must use the real
// thing via EnsureFlatc. This stub only unblocks tests whose assertions
// target templated/asset content (regression_static_test.go, repro_*_test.go).
//
// Build via `go build` in setupMockFlatc; cached across tests via sync.Once.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// pinnedVersion mirrors internal/versions.FlatBuffers without importing it
// (this stub is built standalone, not as part of the xll-gen module). Keep
// in sync with internal/versions/versions.go; a mismatch makes getFlatcVersion
// reject the stub when EnsureFlatc would otherwise pick it up.
const pinnedVersion = "25.9.23"

func main() {
	args := os.Args[1:]
	if len(args) >= 1 && args[0] == "--version" {
		fmt.Println("flatc version " + pinnedVersion)
		return
	}

	var mode, outdir, schema, ns string
	ns = "ipc"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--go":
			mode = "go"
		case "--cpp":
			mode = "cpp"
		case "--go-namespace":
			if i+1 < len(args) {
				ns = args[i+1]
				i++
			}
		case "-o":
			if i+1 < len(args) {
				outdir = args[i+1]
				i++
			}
		default:
			if strings.HasSuffix(args[i], ".fbs") {
				schema = args[i]
			}
		}
	}
	if outdir == "" || schema == "" || mode == "" {
		return
	}
	base := strings.TrimSuffix(filepath.Base(schema), ".fbs")
	if err := os.MkdirAll(outdir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "fake-flatc:", err)
		os.Exit(1)
	}
	var path, content string
	switch mode {
	case "go":
		path = filepath.Join(outdir, base+"_generated.go")
		content = "package " + ns + "\n"
	case "cpp":
		path = filepath.Join(outdir, base+"_generated.h")
		content = "#pragma once\n#include \"protocol_generated.h\"\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "fake-flatc:", err)
		os.Exit(1)
	}
}
