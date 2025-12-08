//go:build tools

// Package tools declares build-time dependencies.
// This file ensures that 'go mod tidy' does not remove dependencies
// that are used by the build process or generated code but not imported
// directly in the main source tree.
package tools

import (
	_ "github.com/xll-gen/shm/go"
)
