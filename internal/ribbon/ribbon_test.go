package ribbon

import (
	"image"
	"image/color"
	"image/png"
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
	got, err := GenerateXML(cfgStructured(), nil)
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
	got, err := GenerateXML(cfg, nil)
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

// writeTestPNG writes a 16x16 PNG (with partial alpha) and returns its path.
func writeTestPNG(t *testing.T, dir, name string) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.Set(x, y, color.NRGBA{R: 220, G: 60, B: 40, A: uint8(255 - y*12)})
		}
	}
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestImagesDedupAndNaming(t *testing.T) {
	dir := t.TempDir()
	writeTestPNG(t, dir, "icons/a.png")
	writeTestPNG(t, dir, "icons/b.png")
	cfg := &config.Config{Ribbon: config.RibbonConfig{Tab: "T", Groups: []config.RibbonGroup{{
		Label: "G",
		Buttons: []config.RibbonButton{
			{Label: "A1", Command: "c1", Image: "./icons/a.png"},
			{Label: "A2", Command: "c1", Image: "icons/a.png"}, // same file, different spelling
			{Label: "B", Command: "c1", Image: "icons/b.png"},
			{Label: "M", Command: "c1", Image: "HappyFace"}, // mso, ignored
		},
	}}}}
	imgs, names, err := Images(cfg, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 2 {
		t.Fatalf("expected 2 deduped images, got %d", len(imgs))
	}
	if imgs[0].Name != "xllgen_img_0" || imgs[1].Name != "xllgen_img_1" {
		t.Fatalf("bad names: %s, %s", imgs[0].Name, imgs[1].Name)
	}
	if len(imgs[0].Data) == 0 || len(imgs[1].Data) == 0 {
		t.Fatal("image data must be loaded")
	}
	if names["./icons/a.png"] != "xllgen_img_0" || names["icons/a.png"] != "xllgen_img_0" {
		t.Fatalf("dedup mapping broken: %v", names)
	}
	if names["icons/b.png"] != "xllgen_img_1" {
		t.Fatalf("second image mapping broken: %v", names)
	}
	if _, ok := names["HappyFace"]; ok {
		t.Fatal("mso value must not appear in the file-image map")
	}
}

func TestImagesErrors(t *testing.T) {
	dir := t.TempDir()
	mk := func(img string) *config.Config {
		return &config.Config{Ribbon: config.RibbonConfig{Tab: "T", Groups: []config.RibbonGroup{{
			Label: "G", Buttons: []config.RibbonButton{{Label: "B", Command: "c", Image: img}},
		}}}}
	}
	// missing file
	if _, _, err := Images(mk("nope.png"), dir); err == nil {
		t.Fatal("expected error for missing file")
	}
	// empty file
	if err := os.WriteFile(filepath.Join(dir, "empty.png"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Images(mk("empty.png"), dir); err == nil {
		t.Fatal("expected error for empty file")
	}
	// oversized file (> 1 MiB)
	if err := os.WriteFile(filepath.Join(dir, "big.png"), make([]byte, 1<<20+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Images(mk("big.png"), dir); err == nil {
		t.Fatal("expected error for oversized file")
	}
}

func TestGenerateXMLFileImages(t *testing.T) {
	cfg := &config.Config{Ribbon: config.RibbonConfig{Tab: "T", Groups: []config.RibbonGroup{{
		Label: "G",
		Buttons: []config.RibbonButton{
			{Label: "F", Command: "c1", Image: "icons/a.png"},
			{Label: "M", Command: "c1", Image: "HappyFace"},
		},
	}}}}
	xmlStr, err := GenerateXML(cfg, map[string]string{"icons/a.png": "xllgen_img_0"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		` loadImage="LoadRibbonImage"`,
		` image="xllgen_img_0"`,
		` imageMso="HappyFace"`,
	} {
		if !strings.Contains(xmlStr, want) {
			t.Errorf("generated XML missing %q:\n%s", want, xmlStr)
		}
	}
}

func TestGenerateXMLNoFileImagesOmitsLoadImage(t *testing.T) {
	cfg := &config.Config{Ribbon: config.RibbonConfig{Tab: "T", Groups: []config.RibbonGroup{{
		Label: "G", Buttons: []config.RibbonButton{{Label: "M", Command: "c1", Image: "HappyFace"}},
	}}}}
	xmlStr, err := GenerateXML(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(xmlStr, "loadImage") {
		t.Errorf("loadImage attribute must be absent without file images:\n%s", xmlStr)
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
