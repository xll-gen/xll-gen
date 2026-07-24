// Package platform centralizes the small set of OS-specific helpers
// xll-gen needs at build / test time. Per AGENTS.md §0.1, the deployed
// runtime is Windows x64 (amd64) only — 32-bit x86 is not supported; this
// package exists so the *developer
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
	"strings"
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

// ExcelPath resolves the host Excel executable's full path from the Windows
// registry (App Paths\excel.exe). It returns the path and true on success, or
// ("", false) when Excel is not registered or the lookup runs on a non-Windows
// developer host. Callers should fall back to a sensible default on false.
func ExcelPath() (string, bool) { return excelPath() }

// securityAddinMarkers are lowercase substrings that identify DLP /
// document-classification Office add-ins in a COM add-in ProgID or
// FriendlyName. These products hook Workbook events (NewWorkbook /
// WorkbookBeforeClose, often with modal classification prompts), which is
// exactly the surface the ribbon temp-workbook bounce touches at xlAutoOpen —
// see IMPROVEMENT_BACKLOG §2 (DLP interop, 2026-07-20) and the ribbon.bounce
// config knob. Matching is advisory-only (doctor WARN), so a rare false
// positive is acceptable. DELIBERATELY generic — product-category terms only,
// no vendor/brand names (repo policy, 2026-07-20): virtually every product in
// this category carries "Classification"/"Classifier", "DLP", or an
// information-protection phrase in its ProgID or FriendlyName, and the
// category terms also avoid matching mainstream non-DLP add-ins (PowerPivot,
// Inquire, Analysis ToolPak...).
var securityAddinMarkers = []string{
	"classif",               // ...Classifier / ...Classification / ...Classify...
	"dlp",                   // ...DLPOfficeAddin / ...DLP.Office...
	"informationprotection", // ...InformationProtection... (label/protect suites)
	"infoprotect",           // abbreviated form of the same
	"datalossprevention",    // spelled-out DLP
}

// MatchesSecurityAddin reports whether a COM add-in identifier (registry key
// name / ProgID, or its FriendlyName) looks like a known DLP/classification
// add-in. Case-insensitive substring match against securityAddinMarkers.
// Exported for doctor and unit-testable without a registry.
func MatchesSecurityAddin(id string) bool {
	lower := strings.ToLower(id)
	for _, m := range securityAddinMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// DetectExcelSecurityAddins enumerates the registered Excel COM add-ins
// (HKCU/HKLM Software\Microsoft\Office\Excel\Addins, including the
// WOW6432Node views) and returns a display line for each one that matches
// MatchesSecurityAddin, e.g. "Acme Classification [AcmeClassifier.Connect] (HKLM)". Returns nil
// when none are found or on a non-Windows developer host. Read-only,
// best-effort: registry errors are treated as "not found".
func DetectExcelSecurityAddins() []string { return detectExcelSecurityAddins() }

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
