package platform

import "testing"

// TestMatchesSecurityAddin pins the DLP/classification add-in marker matching
// used by `xll-gen doctor` (checkExcelSecurityAddins). Positive cases are
// FICTIONAL ProgID/FriendlyName values in the naming shapes this product
// category actually uses (repo policy: no vendor/brand names in the repo —
// the markers are category terms, so category-shaped fixtures exercise them
// fully); negative cases are mainstream Excel add-ins that must never trigger
// the advisory. Context: IMPROVEMENT_BACKLOG §2 (DLP interop, 2026-07-20).
func TestMatchesSecurityAddin(t *testing.T) {
	t.Parallel()

	positives := []string{
		"AcmeClassifier.Connect",                  // "...Classifier..." ProgID shape
		"Acme Message Classification",             // "...Classification" FriendlyName shape
		"Contoso.DlpOfficeAddin",                  // "...Dlp..." ProgID shape
		"Contoso DLP Office Add-in",               // "...DLP..." FriendlyName shape
		"Fabrikam.DataLossPrevention.Excel",       // spelled-out DLP
		"NorthwindInformationProtection.AddIn",    // information-protection suite shape
		"WoodgroveInfoProtect.ExcelAddin",         // abbreviated info-protect shape
		"AcmeDocumentClassification.AddIn",        // generic "classif" marker
		"Contoso Document Classify Helper",        // "...Classify..." verb shape
	}
	for _, id := range positives {
		if !MatchesSecurityAddin(id) {
			t.Errorf("MatchesSecurityAddin(%q) = false, want true", id)
		}
	}

	negatives := []string{
		"PowerPivotExcelClientAddIn.NativeEntry.1", // built-in Microsoft
		"ExcelPlugInShell.PowerMapConnect",         // 3D Maps
		"NativeShim.InquireConnector.1",            // Inquire
		"AnalysisToolPak",                          // ATP
		"MySolver.Addin",                           // generic third-party
		"",                                         // empty FriendlyName probe must not match
	}
	for _, id := range negatives {
		if MatchesSecurityAddin(id) {
			t.Errorf("MatchesSecurityAddin(%q) = true, want false", id)
		}
	}
}

// TestDetectExcelSecurityAddinsIsBestEffort just exercises the enumeration
// end-to-end on the host (registry on Windows, nil stub elsewhere) — it must
// never error/panic regardless of what is installed, and every returned line
// must be non-empty. The content depends on the machine, so no exact match.
func TestDetectExcelSecurityAddinsIsBestEffort(t *testing.T) {
	t.Parallel()
	for _, line := range DetectExcelSecurityAddins() {
		if line == "" {
			t.Errorf("DetectExcelSecurityAddins returned an empty display line")
		}
	}
}
