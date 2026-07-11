//go:build windows

package platform

import (
	"os"

	"golang.org/x/sys/windows/registry"
)

const exeSuffix = ".exe"

// excelPath resolves the Excel executable path from the Windows registry via
// the standard App Paths key (the same mechanism `start excel` uses). It checks
// HKLM first, then HKCU. Returns ("", false) when neither is present or the
// resolved path does not exist on disk.
func excelPath() (string, bool) {
	roots := []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER}
	const sub = `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\excel.exe`
	for _, root := range roots {
		k, err := registry.OpenKey(root, sub, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		// The default (unnamed) value holds the full executable path.
		val, _, err := k.GetStringValue("")
		k.Close()
		if err != nil || val == "" {
			continue
		}
		if _, statErr := os.Stat(val); statErr == nil {
			return val, true
		}
	}
	return "", false
}
