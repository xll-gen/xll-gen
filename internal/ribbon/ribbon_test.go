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
