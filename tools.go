//go:build tools

// Package main declares build-time dependencies.
// This file ensures that 'go mod tidy' does not remove dependencies
// that are used by the build process or generated code but not imported
// directly in the main source tree.
package main

import (
	_ "github.com/xll-gen/shm/go"
	_ "github.com/xll-gen/types/go/protocol"
)
