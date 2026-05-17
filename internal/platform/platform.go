// Package platform centralizes the small set of OS-specific helpers
// xll-gen needs at build / test time. Per AGENTS.md §0.1, the deployed
// runtime is Windows x86/x64 only; this package exists so the *developer
// tooling* (CLI, regtest, smoketest) can compile and run on Linux/macOS
// for unit tests without scattering `runtime.GOOS == "windows"` branches
// across every file that needs an executable suffix.
//
// All Windows-specific knowledge lives in platform_windows.go; the !windows
// fallback lives in platform_other.go. Add new helpers here only when the
// alternative is duplicating a runtime.GOOS check in ≥2 places.
package platform

import (
	"fmt"
	"os"
	"path/filepath"
)

// ExeName returns the platform-specific executable name for `base`. On
// Windows this appends ".exe"; on other OSes the name is returned
// unchanged. Pass just the basename — directory components stay intact:
//
//	platform.ExeName("flatc")            // "flatc.exe" on Windows
//	platform.ExeName(filepath.Join(b,"x")) // "<b>/x.exe" on Windows
func ExeName(base string) string {
	if exeSuffix == "" {
		return base
	}
	return base + exeSuffix
}

// ExeSuffix returns ".exe" on Windows and "" elsewhere. Prefer ExeName when
// you have a name to extend; ExeSuffix is for the rare case where you need
// the literal suffix (e.g. building a "<base>.exe" alongside a "<base>"
// variant in the same loop).
func ExeSuffix() string { return exeSuffix }

// FindBuiltExe locates an executable that cmake produced under `buildDir`.
// CMake's output layout differs between single-config generators (Make,
// Ninja, MinGW Makefiles — output at `buildDir/<name>`) and multi-config
// generators (Visual Studio / MSVC — output at `buildDir/<Config>/<name>`).
// FindBuiltExe checks both, returning the first hit's absolute path, or an
// error listing every location it tried. `name` should be the base name
// without an extension; the function appends ExeSuffix internally.
//
// Used by regtest and smoketest, both of which invoke cmake without
// pinning a single-config generator and must therefore tolerate either
// layout.
func FindBuiltExe(buildDir, name string) (string, error) {
	exe := ExeName(name)
	candidates := []string{
		filepath.Join(buildDir, exe),
		filepath.Join(buildDir, "Release", exe),
		filepath.Join(buildDir, "Debug", exe),
	}
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if _, err := os.Stat(abs); err == nil {
			return abs, nil
		}
	}
	return "", fmt.Errorf("platform: %q not found under %q; tried %v", exe, buildDir, candidates)
}
