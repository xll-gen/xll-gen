package generator

import (
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
)

// ribbonRtdCfg is ribbonConnectCfg() plus an RTD server, i.e. the shape in which
// BOTH halves of DllUnregisterServer (RTD + ribbon) must be emitted.
func ribbonRtdCfg() *config.Config {
	cfg := ribbonConnectCfg()
	cfg.Functions = []config.Function{{
		Name: "Stream", Mode: "rtd", Return: "string",
		Args: []config.Arg{{Name: "a", Type: "int"}},
	}}
	cfg.Rtd = config.RtdConfig{
		Enabled:     true,
		ProgID:      "TestProj.Rtd",
		Clsid:       "{11111111-2222-3333-4444-555555555555}",
		Description: "t",
	}
	return cfg
}

// hookBody returns the comment-stripped GracefulComTeardownHook body from a
// rendered xll_main.cpp. Same window-carving discipline as
// gen_office_disconnect_guard_test.go.
func hookBody(t *testing.T, src string) string {
	t.Helper()
	code := stripCppComments(src)
	i := strings.Index(code, "static void GracefulComTeardownHook(bool revokeRtdClassObject)")
	if i < 0 {
		t.Fatalf("GracefulComTeardownHook not emitted")
	}
	hook := code[i:]
	if e := strings.Index(hook, "\nextern \"C\""); e > 0 {
		hook = hook[:e]
	}
	return hook
}

// TestTeardownHookDoesNotUnregisterTheAddinKey pins the fix for backlog line 120:
// "after a COM add-in disable, does the entry come back in the COM Add-ins UI
// list?" It did NOT, and the reason was decidable from the code without Excel.
//
// `HKCU\Software\Microsoft\Office\Excel\Addins\<progId>` IS the row source of the
// File▸Options▸Add-Ins▸COM Add-Ins dialog. `GracefulComTeardownHook` used to
// `RegDeleteTreeW` that key (rtd::UnregisterOfficeAddinKey) plus the whole COM
// registration (rtd::UnregisterServer -> HKCU\Software\Classes\<progId> and
// CLSID\{...} incl. InprocServer32) on EVERY confirmed teardown — including the
// add-in-DISABLE teardown that the dialog's own untick drives. So unticking the
// box deleted the row that would let the user tick it back, and even a row
// surviving from another hive had nothing left to activate.
//
// The Addins key's lifetime is therefore "INSTALLED", not "SESSION": it is
// created by TryConnectRibbon (LoadBehavior=0 on a fresh install, so nothing
// autoloads at startup — the key is inert until something connects it; an existing
// LoadBehavior is preserved from 2026-08-03 on, see
// internal/assets/registry_addin_key_cpp_test.go) and removed only by
// DllUnregisterServer, the documented uninstall entry point.
//
// WHAT MUST NOT MOVE. Everything ABOVE the deleted lines is the 2026-07-30
// mso.dll NULL-vtable crash fix (Confirmed-Correct Decisions, "Office add-in
// disconnect re-entrancy"): the OfficeDisconnectInProgress() skip, the
// SetRibbonConnected(false) in its else branch, and CoRevokeClassObject in that
// ORDER. Asserted here as well as in gen_office_disconnect_guard_test.go, because
// this test is the one that edits that block's neighbourhood.
func TestTeardownHookDoesNotUnregisterTheAddinKey(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		cfg  *config.Config
	}{
		{"ribbon+commands", ribbonConnectCfg()},
		{"ribbon+commands+rtd", ribbonRtdCfg()},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			hook := hookBody(t, renderCppMain(t, tc.cfg))

			// (i) the confirmed-correct crash-fix block, in order, untouched.
			guardIdx := strings.Index(hook, "xll::ribbon::OfficeDisconnectInProgress()")
			discIdx := strings.Index(hook, "SetRibbonConnected(false)")
			revokeIdx := strings.Index(hook, "CoRevokeClassObject(g_ribbonCookie)")
			if guardIdx < 0 || discIdx < 0 || revokeIdx < 0 {
				t.Fatalf("the hook must still contain OfficeDisconnectInProgress() / "+
					"SetRibbonConnected(false) / CoRevokeClassObject(g_ribbonCookie) "+
					"(got %d/%d/%d)\n---\n%s", guardIdx, discIdx, revokeIdx, hook)
			}
			if !(guardIdx < discIdx && discIdx < revokeIdx) {
				t.Errorf("the §22 crash-fix ORDER must hold: OfficeDisconnectInProgress@%d < "+
					"SetRibbonConnected(false)@%d < CoRevokeClassObject@%d\n---\n%s",
					guardIdx, discIdx, revokeIdx, hook)
			}

			// (ii) …and it must NOT delete the Addins key or the COM registration.
			if strings.Contains(hook, "UnregisterOfficeAddinKey") {
				t.Errorf("GracefulComTeardownHook must NOT call rtd::UnregisterOfficeAddinKey: that key is "+
					"the COM Add-ins dialog's row source, so deleting it on an add-in DISABLE removes the "+
					"very row the user needs to tick the add-in back on. Its lifetime is INSTALLED "+
					"(DllUnregisterServer), not SESSION\n---\n%s", hook)
			}
			if strings.Contains(hook, "UnregisterServer(GetRibbonClsid()") {
				t.Errorf("GracefulComTeardownHook must NOT call rtd::UnregisterServer for the ribbon CLSID: "+
					"it deletes HKCU\\Software\\Classes\\<progId> and CLSID\\{...}\\InprocServer32, so a "+
					"surviving dialog row would have nothing to activate\n---\n%s", hook)
			}
		})
	}
}

// TestDllUnregisterServerRemovesTheAddinKey is the other half: with the teardown
// no longer deleting them, the ONLY documented removal point must actually remove
// them — and it has to exist for a ribbon project even when RTD is off (today the
// whole extern "C" DllUnregisterServer was gated on .Rtd.Enabled alone).
func TestDllUnregisterServerRemovesTheAddinKey(t *testing.T) {
	t.Parallel()

	body := func(t *testing.T, cfg *config.Config) string {
		t.Helper()
		code := stripCppComments(renderCppMain(t, cfg))
		i := strings.Index(code, "DllUnregisterServer()")
		if i < 0 {
			t.Fatalf("DllUnregisterServer must be emitted for this project shape "+
				"— it is the documented uninstall entry point that removes the Excel Addins key\n---\n%s", code)
		}
		fn := code[i:]
		if e := strings.Index(fn, "\n    }"); e > 0 {
			fn = fn[:e]
		}
		return fn
	}

	t.Run("ribbon without rtd", func(t *testing.T) {
		t.Parallel()
		fn := body(t, ribbonConnectCfg())
		for _, want := range []string{
			"rtd::UnregisterOfficeAddinKey(g_szRibbonProgID)",
			"rtd::UnregisterServer(GetRibbonClsid(), g_szRibbonProgID)",
		} {
			if !strings.Contains(fn, want) {
				t.Errorf("DllUnregisterServer must contain %q\n---\n%s", want, fn)
			}
		}
		if strings.Contains(fn, "GetRtdClsid()") {
			t.Errorf("no RTD half may be emitted for a ribbon-only project\n---\n%s", fn)
		}
	})

	t.Run("ribbon with rtd", func(t *testing.T) {
		t.Parallel()
		fn := body(t, ribbonRtdCfg())
		for _, want := range []string{
			"rtd::UnregisterServer(GetRtdClsid(), g_szProgID)",
			"rtd::UnregisterOfficeAddinKey(g_szRibbonProgID)",
			"rtd::UnregisterServer(GetRibbonClsid(), g_szRibbonProgID)",
		} {
			if !strings.Contains(fn, want) {
				t.Errorf("DllUnregisterServer must contain %q\n---\n%s", want, fn)
			}
		}
	})
}
