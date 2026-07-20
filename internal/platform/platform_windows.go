//go:build windows

package platform

import (
	"fmt"
	"os"
	"sort"
	"strings"

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

// detectExcelSecurityAddins enumerates every registered Excel COM add-in key
// under both hives and both registry views and returns one display line per
// add-in whose key name (ProgID) or FriendlyName matches a known
// DLP/classification marker (MatchesSecurityAddin). Read-only, best-effort:
// any registry error just skips that hive. Duplicate ProgIDs registered in
// several hives collapse to one line listing all hives.
func detectExcelSecurityAddins() []string {
	type hive struct {
		root  registry.Key
		label string
		sub   string
	}
	const addins = `Software\Microsoft\Office\Excel\Addins`
	hives := []hive{
		{registry.CURRENT_USER, "HKCU", addins},
		{registry.LOCAL_MACHINE, "HKLM", addins},
		// 32-bit add-ins registered on 64-bit Windows land under WOW6432Node.
		{registry.LOCAL_MACHINE, "HKLM/WOW64", `Software\WOW6432Node\Microsoft\Office\Excel\Addins`},
		{registry.CURRENT_USER, "HKCU/WOW64", `Software\WOW6432Node\Microsoft\Office\Excel\Addins`},
	}

	found := map[string][]string{} // display name -> hive labels
	for _, h := range hives {
		k, err := registry.OpenKey(h.root, h.sub, registry.ENUMERATE_SUB_KEYS)
		if err != nil {
			continue
		}
		progIDs, err := k.ReadSubKeyNames(-1)
		k.Close()
		if err != nil {
			continue
		}
		for _, progID := range progIDs {
			display := progID
			friendly := ""
			if sk, err := registry.OpenKey(h.root, h.sub+`\`+progID, registry.QUERY_VALUE); err == nil {
				if v, _, err := sk.GetStringValue("FriendlyName"); err == nil && v != "" {
					friendly = v
					display = fmt.Sprintf("%s [%s]", v, progID)
				}
				sk.Close()
			}
			if MatchesSecurityAddin(progID) || MatchesSecurityAddin(friendly) {
				found[display] = append(found[display], h.label)
			}
		}
	}
	if len(found) == 0 {
		return nil
	}

	lines := make([]string, 0, len(found))
	for display, hiveLabels := range found {
		lines = append(lines, fmt.Sprintf("%s (%s)", display, strings.Join(hiveLabels, ", ")))
	}
	sort.Strings(lines)
	return lines
}
