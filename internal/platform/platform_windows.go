//go:build windows

package platform

import (
	"fmt"
	"os"
	"path/filepath"
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

// excelTrustedLocationFor reports which Trusted Location entry (if any) covers
// `dir`, returning the entry's Path and true on a hit.
//
// This is the check that actually predicts whether Excel will load a built XLL.
// Measured 2026-08-03 with a 2x2 that varied path length and trusted-location
// membership independently, on one machine, with RequireAddinSig=0 and an empty
// DisabledItems: a 31-character path OUTSIDE a trusted location did not load (no
// native log, no server process), and a 168-character path INSIDE one did. The
// earlier "long paths fail" reading came from an A/B that moved both variables at
// once -- its short path also happened to be inside the trusted root.
//
// An entry covers `dir` when `dir` equals the entry's Path, or when
// AllowSubfolders is non-zero and `dir` is under it. Excel resolves the machine
// policy hive too; this reads only the per-user hive Excel writes from the Trust
// Center UI, which is where a developer's own entry lands. Read-only,
// best-effort: any registry error is treated as "no entry", so a false NEGATIVE
// (we say untrusted when a policy hive trusts it) is possible and a false
// positive is not. That asymmetry is deliberate -- the caller only WARNS.
func excelTrustedLocationFor(dir string) (string, bool) {
	// filepath.Abs("") resolves to the process CWD, which would silently turn
	// "no directory given" into "is the current directory trusted?" — a wrong
	// ANSWER rather than a refusal to answer.
	if strings.TrimSpace(dir) == "" {
		return "", false
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	// Excel keys Trusted Locations under the Office major version. Probe the
	// versions that ship a 16.x Excel rather than hardcoding one, so a machine on
	// a different build is not silently reported as untrusted.
	for _, ver := range []string{"16.0", "15.0", "14.0"} {
		sub := `Software\Microsoft\Office\` + ver + `\Excel\Security\Trusted Locations`
		k, err := registry.OpenKey(registry.CURRENT_USER, sub, registry.ENUMERATE_SUB_KEYS)
		if err != nil {
			continue
		}
		names, err := k.ReadSubKeyNames(-1)
		k.Close()
		if err != nil {
			continue
		}
		for _, name := range names {
			sk, err := registry.OpenKey(registry.CURRENT_USER, sub+`\`+name, registry.QUERY_VALUE)
			if err != nil {
				continue
			}
			p, _, perr := sk.GetStringValue("Path")
			allowSub, _, aerr := sk.GetIntegerValue("AllowSubfolders")
			sk.Close()
			if perr != nil || p == "" {
				continue
			}
			entry, err := filepath.Abs(os.ExpandEnv(p))
			if err != nil {
				continue
			}
			if pathCoveredByTrustedEntry(entry, aerr == nil && allowSub != 0, abs) {
				return p, true
			}
		}
	}
	return "", false
}

// pathCoveredByTrustedEntry decides whether a Trusted Locations entry covers
// `dir`. Split out from the registry walk so the decision — the only part with a
// trap in it — is testable without a registry.
//
// The trap is the prefix compare: a naive strings.HasPrefix makes the entry
// "C:\foo" cover "C:\foobar", which would report a directory as trusted
// when Excel does not treat it that way -- the one direction this check must
// never get wrong, since it would restore exactly the false green the old
// path-length warning gave. Terminating both sides with a separator prevents it.
//
// Trailing separators are trimmed before the exact compare because Excel writes
// Trusted Location paths WITH one while os.Getwd() never returns one. That is
// belt-and-braces for the registry caller, which already passes both sides
// through filepath.Abs (hence Clean): the function is made correct on its own
// terms rather than left depending on its caller to normalize.
//
// Comparison is case-insensitive because Windows paths are.
func trimTrailingSep(p string) string {
	for len(p) > 1 && (p[len(p)-1] == '\\' || p[len(p)-1] == '/') {
		p = p[:len(p)-1]
	}
	return p
}

func pathCoveredByTrustedEntry(entry string, allowSubfolders bool, dir string) bool {
	if entry == "" || dir == "" {
		return false
	}
	// Excel writes Trusted Location Paths WITH a trailing separator (observed:
	// "C:\Users\...\xll-gen\\"), while os.Getwd() never returns one. Comparing
	// them raw made an exact-match entry read as a miss; it only looked fine
	// because AllowSubfolders happened to be set on the entry that was tested.
	if strings.EqualFold(trimTrailingSep(entry), trimTrailingSep(dir)) {
		return true
	}
	if !allowSubfolders {
		return false
	}
	sep := string(filepath.Separator)
	prefix := strings.ToLower(entry)
	if !strings.HasSuffix(prefix, sep) {
		prefix += sep
	}
	return strings.HasPrefix(strings.ToLower(dir)+sep, prefix)
}
