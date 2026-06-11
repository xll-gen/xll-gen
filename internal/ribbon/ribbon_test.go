package ribbon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
)

func cfgStructured() *config.Config {
	cfg := &config.Config{
		Project:  config.ProjectConfig{Name: "demo"},
		Commands: []config.Command{{Name: "RunReport"}, {Name: "ClearAll"}},
		Ribbon: config.RibbonConfig{
			Tab: "My <Tools>", // angle brackets must be escaped in output
			Groups: []config.RibbonGroup{
				{Label: "Reports", Buttons: []config.RibbonButton{
					{Label: "Monthly", Command: "RunReport", Size: "large", Image: "FileSave"},
					{Label: "Clear", Command: "ClearAll", Size: "normal"},
				}},
			},
		},
	}
	config.ApplyDefaults(cfg)
	return cfg
}

func TestGenerateXMLGolden(t *testing.T) {
	got, err := GenerateXML(cfgStructured())
	if err != nil {
		t.Fatal(err)
	}
	want := `<customUI xmlns="http://schemas.microsoft.com/office/2006/01/customui"><ribbon><tabs><tab id="xllgen_tab" label="My &lt;Tools&gt;"><group id="xllgen_grp_0" label="Reports"><button id="xllgen_btn_0_0" label="Monthly" size="large" onAction="RunReport" imageMso="FileSave"/><button id="xllgen_btn_0_1" label="Clear" size="normal" onAction="ClearAll"/></group></tab></tabs></ribbon></customUI>`
	if got != want {
		t.Errorf("xml mismatch:\n got: %s\nwant: %s", got, want)
	}
}

// TestGenerateXMLSizeDefaultLocal verifies GenerateXML defaults an empty
// button Size to "normal" on its own, without relying on config.ApplyDefaults
// having been run upstream. The cfg here is hand-built and ApplyDefaults is
// deliberately NOT called.
func TestGenerateXMLSizeDefaultLocal(t *testing.T) {
	cfg := &config.Config{
		Project:  config.ProjectConfig{Name: "demo"},
		Commands: []config.Command{{Name: "RunReport"}},
		Ribbon: config.RibbonConfig{
			Tab: "Tools",
			Groups: []config.RibbonGroup{
				{Label: "G", Buttons: []config.RibbonButton{
					{Label: "B", Command: "RunReport"}, // Size deliberately empty
				}},
			},
		},
	}
	got, err := GenerateXML(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `size="normal"`) {
		t.Errorf("empty Size not defaulted to normal locally:\n%s", got)
	}
}

func TestValidateRawXML(t *testing.T) {
	dir := t.TempDir()
	cmds := []config.Command{{Name: "RunReport"}}

	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	good := write("good.xml", `<customUI xmlns="http://schemas.microsoft.com/office/2006/01/customui"><ribbon><tabs><tab id="t" label="T"><group id="g" label="G"><button id="b" label="B" onAction="RunReport"/></group></tab></tabs></ribbon></customUI>`)
	if _, err := ValidateRawXML(good, cmds); err != nil {
		t.Errorf("good xml rejected: %v", err)
	}

	bad := write("bad.xml", `<customUI xmlns="http://schemas.microsoft.com/office/2006/01/customui"><ribbon><tabs><tab id="t" label="T"><group id="g" label="G"><button id="b" label="B" onAction="NoSuchCommand"/></group></tab></tabs></ribbon></customUI>`)
	if _, err := ValidateRawXML(bad, cmds); err == nil || !strings.Contains(err.Error(), "NoSuchCommand") {
		t.Errorf("dangling onAction not rejected: %v", err)
	}

	dyn := write("dyn.xml", `<customUI xmlns="http://schemas.microsoft.com/office/2006/01/customui"><ribbon><tabs><tab id="t" label="T"><group id="g" label="G"><button id="b" getLabel="GetIt" onAction="RunReport"/></group></tab></tabs></ribbon></customUI>`)
	if _, err := ValidateRawXML(dyn, cmds); err == nil || !strings.Contains(err.Error(), "getLabel") {
		t.Errorf("dynamic callback not rejected in v1: %v", err)
	}

	// Malformed XML must surface a parse error.
	malformed := write("malformed.xml", `<customUI><unclosed`)
	if _, err := ValidateRawXML(malformed, cmds); err == nil || !strings.Contains(err.Error(), "parse error") {
		t.Errorf("malformed xml not reported as parse error: %v", err)
	}

	// A BOM-prefixed good file: the xml decoder tolerates the BOM so validation
	// succeeds, and the returned string still carries the BOM. ToCppRawLiteral
	// strips that BOM downstream before embedding the literal.
	const bomPrefix = "\uFEFF"
	bomContent := bomPrefix + `<customUI xmlns="http://schemas.microsoft.com/office/2006/01/customui"><ribbon><tabs><tab id="t" label="T"><group id="g" label="G"><button id="b" label="B" onAction="RunReport"/></group></tab></tabs></ribbon></customUI>`
	bom := write("bom.xml", bomContent)
	out, err := ValidateRawXML(bom, cmds)
	if err != nil {
		t.Errorf("BOM-prefixed good xml rejected: %v", err)
	}
	if !strings.HasPrefix(out, bomPrefix) {
		t.Errorf("returned string should still carry the BOM (stripped later by ToCppRawLiteral); got prefix %q", out[:min(6, len(out))])
	}
}

func TestToCppRawLiteral(t *testing.T) {
	lit, err := ToCppRawLiteral(`<customUI a="b"/>`)
	if err != nil {
		t.Fatal(err)
	}
	want := `LR"XLLRIBBON(<customUI a="b"/>)XLLRIBBON"`
	if lit != want {
		t.Errorf("got %s want %s", lit, want)
	}
	if _, err := ToCppRawLiteral(`evil )XLLRIBBON" injection`); err == nil {
		t.Error("raw-literal delimiter collision not rejected")
	}
}

// TestToCppRawLiteralKorean verifies non-ASCII XML content (a primary use case:
// Korean labels) is carried as XML numeric character references, keeping the
// emitted C++ literal pure ASCII. 내=U+B0B4, 도=U+B3C4, 구=U+AD6C; the space
// between words is preserved verbatim.
func TestToCppRawLiteralKorean(t *testing.T) {
	lit, err := ToCppRawLiteral(`<button label="내 도구"/>`)
	if err != nil {
		t.Fatal(err)
	}
	want := `LR"XLLRIBBON(<button label="&#xB0B4; &#xB3C4;&#xAD6C;"/>)XLLRIBBON"`
	if lit != want {
		t.Errorf("korean ncr mismatch:\n got: %s\nwant: %s", lit, want)
	}
	if !strings.Contains(lit, "&#xB0B4;") {
		t.Errorf("expected NCR for 내 (U+B0B4): %s", lit)
	}
	if !strings.Contains(lit, "&#xB3C4;&#xAD6C;") {
		t.Errorf("expected NCRs for 도구 (U+B3C4 U+AD6C): %s", lit)
	}
	for _, r := range lit {
		if r > 0x7F {
			t.Errorf("literal contains non-ASCII rune %U: %s", r, lit)
		}
	}
}

// TestToCppRawLiteralBOMStrip verifies a leading UTF-8 BOM is stripped so it
// never corrupts the embedded ribbon XML.
func TestToCppRawLiteralBOMStrip(t *testing.T) {
	lit, err := ToCppRawLiteral("\uFEFF<a/>")
	if err != nil {
		t.Fatal(err)
	}
	want := `LR"XLLRIBBON(<a/>)XLLRIBBON"`
	if lit != want {
		t.Errorf("BOM not stripped: got %s want %s", lit, want)
	}
}

// TestToCppRawLiteralBenignSubstrings verifies the delimiter-collision check
// only rejects the exact closing sequence, not benign substrings of it.
func TestToCppRawLiteralBenignSubstrings(t *testing.T) {
	for _, in := range []string{`<a b=")"/>`, `<a b="XLLRIBBON"/>`} {
		if _, err := ToCppRawLiteral(in); err != nil {
			t.Errorf("benign substring falsely rejected (%q): %v", in, err)
		}
	}
}
