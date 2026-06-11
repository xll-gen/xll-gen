# Native Ribbon + Command Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
> **Model directive:** All implementation AND verification subagents must run with `model: "opus"` (user directive, see memory `project-ribbon-design`).

**Goal:** Excel ribbon buttons (and keyboard-shortcut commands) declared in `xll.yaml` invoke user-written Go handlers, which may use `sugar` to manipulate the running Excel.

**Architecture:** The XLL doubles as a COM add-in helper (Excel-DNA mechanism): `xlAutoOpen` registers HKCU COM keys + an Office Addins key, `CoRegisterClassObject`s a new `RibbonAddIn` class (reusing the RTD COM skeleton), and connects it via `Application.COMAddIns`. Excel calls `IRibbonExtensibility::GetCustomUI` for embedded XML; button clicks arrive via `IDispatch::Invoke` and are dispatched fire-and-forget over the existing SHM IPC (`MSG_COMMAND_INVOKE = 137`) to the Go server, which runs the handler in a panic-recovered goroutine. Commands are additionally registered as XLL commands (`xlfRegister` macroType=2) so shortcuts work even when HKCU registration is blocked (graceful degradation).

**Tech Stack:** Go (config/codegen/server), C++17 (XLL/COM assets), FlatBuffers (protocol), text/template codegen.

**Spec:** `docs/superpowers/specs/2026-06-11-ribbon-command-design.md`

**Repo root for all paths below:** `C:\Users\minje\Nextcloud\devprj\xll-gen\xll-gen` (sibling repos: `..\types`, `..\sugar`)

---

## File Structure

| File | Action | Responsibility |
|---|---|---|
| `internal/config/config.go` | Modify | `Command`, `RibbonButton`, `RibbonGroup`, `RibbonConfig` types; validation; defaults (ProgID/CLSID derivation) |
| `internal/config/config_test.go` | Modify/Create | Table tests for new validation rules |
| `internal/ribbon/ribbon.go` | Create | customUI XML generation (structured mode), raw-XML onAction validation, C++ raw-literal embedding |
| `internal/ribbon/ribbon_test.go` | Create | Golden XML test, validation tests |
| `internal/templates/protocol.fbs` | Modify | `CommandInvokeRequest`/`CommandInvokeResponse` tables |
| `../types` repo `protocol.fbs` + regen | Modify | Same tables; regenerate Go + C++ |
| `internal/assets/files/include/xll_ipc.h` | Modify | `MSG_COMMAND_INVOKE 137` |
| `pkg/server/types.go` | Modify | `MsgCommandInvoke = 137`, `CommandContext` |
| `pkg/server/handlers.go` | Modify | `HandleCommandInvoke` |
| `pkg/server/handlers_command_test.go` | Create | Routing/panic/unknown-name tests |
| `internal/templates/interface.go.tmpl` | Modify | Command methods on `XllService` |
| `internal/templates/server.go.tmpl` | Modify | `MsgCommandInvoke` dispatch case |
| `internal/generator/gen_go.go`, `gen_common.go` | Modify | Pass `Commands` to Go templates |
| `internal/generator/gen_cpp.go` | Modify | Pass `Commands`/`Ribbon`/ribbon XML to C++ templates; emit `ribbon_xml.h` |
| `internal/assets/files/include/com/extensibility.h` | Create | `IDTExtensibility2` + `IRibbonExtensibility` interface/IID definitions |
| `internal/assets/files/include/com/dispatch_helpers.h` | Create | Tiny IDispatch get/put/call helpers (header-only) |
| `internal/assets/files/include/com/ribbon_addin.h` | Create | `RibbonAddIn` class declaration + state setters |
| `internal/assets/files/src/ribbon_addin.cpp` | Create | `RibbonAddIn` implementation + fire-and-forget IPC send |
| `internal/assets/files/include/rtd/registry.h` | Modify | `RegisterOfficeAddinKey` / `UnregisterOfficeAddinKey` helpers |
| `internal/templates/xll_main.cpp.tmpl` | Modify | DllGetClassObject ribbon branch, command procs, macroType=2 registration, xlAutoOpen connect + xlAutoClose disconnect |
| `internal/templates/CMakeLists.txt.tmpl` | Modify | Compile `ribbon_addin.cpp`, link `oleacc`, define `XLL_RIBBON_ENABLED` |
| `internal/templates/regtest_main.cpp.tmpl` | Modify | Assert macroType=2 for commands |
| `AGENTS.md` (xll-gen), `../sugar/AGENTS.md` | Modify | Co-change cluster; `GetApplicationByPID` backlog row |

Notes for all C++ template work: the `{{if .Rtd.Enabled}}` guard around `DllGetClassObject`/`DllCanUnloadNow` in `xll_main.cpp.tmpl:73-106` must become "RTD **or** Ribbon" — a ribbon-only project still needs those exports.

---

### Task 1: Config types, validation, defaults

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go` (create if absent; if a config test file already exists under a different name, extend that one)

- [ ] **Step 1: Write failing tests**

Append to (or create) `internal/config/config_test.go`:

```go
package config

import (
	"strings"
	"testing"
)

func baseCfg() *Config {
	return &Config{
		Project: ProjectConfig{Name: "demo"},
		Functions: []Function{
			{Name: "MyFunc", Return: "float"},
		},
	}
}

func TestCommandValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string // substring; "" = expect success
	}{
		{
			name: "valid command without ribbon",
			mutate: func(c *Config) {
				c.Commands = []Command{{Name: "RunReport"}}
			},
		},
		{
			name: "command name collides with function name",
			mutate: func(c *Config) {
				c.Commands = []Command{{Name: "MyFunc"}}
			},
			wantErr: "collides",
		},
		{
			name: "duplicate command names",
			mutate: func(c *Config) {
				c.Commands = []Command{{Name: "A"}, {Name: "A"}}
			},
			wantErr: "duplicate command",
		},
		{
			name: "shortcut must be single letter",
			mutate: func(c *Config) {
				c.Commands = []Command{{Name: "A", Shortcut: "Ctrl+Shift+R"}}
			},
			wantErr: "shortcut",
		},
		{
			name: "ribbon without commands",
			mutate: func(c *Config) {
				c.Ribbon = RibbonConfig{Tab: "Tools"}
			},
			wantErr: "ribbon requires",
		},
		{
			name: "ribbon structured and raw xml are mutually exclusive",
			mutate: func(c *Config) {
				c.Commands = []Command{{Name: "A"}}
				c.Ribbon = RibbonConfig{Tab: "Tools", XML: "ribbon.xml"}
			},
			wantErr: "mutually exclusive",
		},
		{
			name: "button references unknown command",
			mutate: func(c *Config) {
				c.Commands = []Command{{Name: "A"}}
				c.Ribbon = RibbonConfig{Tab: "Tools", Groups: []RibbonGroup{
					{Label: "G", Buttons: []RibbonButton{{Label: "B", Command: "Nope"}}},
				}}
			},
			wantErr: "unknown command",
		},
		{
			name: "invalid button size",
			mutate: func(c *Config) {
				c.Commands = []Command{{Name: "A"}}
				c.Ribbon = RibbonConfig{Tab: "Tools", Groups: []RibbonGroup{
					{Label: "G", Buttons: []RibbonButton{{Label: "B", Command: "A", Size: "huge"}}},
				}}
			},
			wantErr: "size",
		},
		{
			name: "valid structured ribbon",
			mutate: func(c *Config) {
				c.Commands = []Command{{Name: "A"}}
				c.Ribbon = RibbonConfig{Tab: "Tools", Groups: []RibbonGroup{
					{Label: "G", Buttons: []RibbonButton{{Label: "B", Command: "A", Size: "large"}}},
				}}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseCfg()
			tt.mutate(cfg)
			err := Validate(cfg)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tt.wantErr, err)
			}
		})
	}
}

func TestCommandDefaults(t *testing.T) {
	cfg := baseCfg()
	cfg.Commands = []Command{{Name: "RunReport"}}
	cfg.Ribbon = RibbonConfig{Tab: "Tools", Groups: []RibbonGroup{
		{Label: "G", Buttons: []RibbonButton{{Label: "B", Command: "RunReport"}}},
	}}
	ApplyDefaults(cfg)

	if cfg.Commands[0].Handler != "RunReport" {
		t.Errorf("handler default: got %q", cfg.Commands[0].Handler)
	}
	if cfg.Ribbon.ProgID != "demo.Ribbon" {
		t.Errorf("ribbon prog_id default: got %q", cfg.Ribbon.ProgID)
	}
	if cfg.Ribbon.Clsid == "" || cfg.Ribbon.Clsid[0] != '{' {
		t.Errorf("ribbon clsid not derived: got %q", cfg.Ribbon.Clsid)
	}
	// Deterministic: same ProgID -> same CLSID
	cfg2 := baseCfg()
	cfg2.Commands = cfg.Commands
	cfg2.Ribbon = RibbonConfig{Tab: "Tools", Groups: cfg.Ribbon.Groups}
	ApplyDefaults(cfg2)
	if cfg2.Ribbon.Clsid != cfg.Ribbon.Clsid {
		t.Errorf("clsid not deterministic: %q vs %q", cfg.Ribbon.Clsid, cfg2.Ribbon.Clsid)
	}
	if cfg.Ribbon.Groups[0].Buttons[0].Size != "normal" {
		t.Errorf("button size default: got %q", cfg.Ribbon.Groups[0].Buttons[0].Size)
	}
}

func TestRibbonEnabled(t *testing.T) {
	r := RibbonConfig{}
	if r.Enabled() {
		t.Error("empty ribbon should be disabled")
	}
	if !(RibbonConfig{Tab: "T"}).Enabled() {
		t.Error("tab mode should be enabled")
	}
	if !(RibbonConfig{XML: "r.xml"}).Enabled() {
		t.Error("xml mode should be enabled")
	}
}
```

- [ ] **Step 2: Run tests, verify they fail to compile (types missing)**

Run: `cd C:\Users\minje\Nextcloud\devprj\xll-gen\xll-gen && go test ./internal/config/`
Expected: FAIL — `undefined: Command`, `undefined: RibbonConfig`, etc.

- [ ] **Step 3: Add types to `internal/config/config.go`**

After the `Function`-related types (below `Arg`, around line 196), add:

```go
// Command represents a user-defined Excel command (macro), invocable from
// ribbon buttons, a Ctrl+Shift shortcut, or by typing its name in the
// Alt+F8 dialog (XLL commands are runnable there but not listed).
type Command struct {
	// Name is the command name registered with Excel (xlfRegister, macroType=2).
	Name string `yaml:"name"`
	// Description is the help text for the command.
	Description string `yaml:"description"`
	// Handler is the Go method name on XllService. Defaults to Name.
	Handler string `yaml:"handler"`
	// Shortcut is a single letter; Excel binds it as Ctrl+Shift+<letter>.
	Shortcut string `yaml:"shortcut"`
}

// RibbonButton is one button in a structured-mode ribbon group.
type RibbonButton struct {
	// Label is the button caption.
	Label string `yaml:"label"`
	// Command is the name of the Command this button invokes (onAction).
	Command string `yaml:"command"`
	// Size is "large" or "normal" (default "normal").
	Size string `yaml:"size"`
	// Image is an optional imageMso name.
	Image string `yaml:"image"`
}

// RibbonGroup is one group of buttons in a structured-mode ribbon tab.
type RibbonGroup struct {
	// Label is the group caption.
	Label string `yaml:"label"`
	// Buttons is the list of buttons in this group.
	Buttons []RibbonButton `yaml:"buttons"`
}

// RibbonConfig declares the add-in's custom ribbon UI. Two mutually
// exclusive modes: structured (Tab + Groups, XML is generated) or raw
// (XML names a customUI XML file authored by the user).
type RibbonConfig struct {
	// Tab is the custom tab label (structured mode).
	Tab string `yaml:"tab"`
	// Groups are the button groups under Tab (structured mode).
	Groups []RibbonGroup `yaml:"groups"`
	// XML is a path to a raw customUI XML file, relative to xll.yaml (raw mode).
	XML string `yaml:"xml"`
	// ProgID identifies the COM add-in helper (default "<project>.Ribbon").
	ProgID string `yaml:"prog_id"`
	// Clsid is the helper's class ID (derived from ProgID if empty).
	Clsid string `yaml:"clsid"`
}

// Enabled reports whether a ribbon UI was declared in either mode.
func (r RibbonConfig) Enabled() bool {
	return r.Tab != "" || r.XML != ""
}
```

Add the fields to `Config` (after `Rtd`):

```go
	// Commands is a list of Excel commands (macros) backed by Go handlers.
	Commands  []Command    `yaml:"commands"`
	// Ribbon declares the custom ribbon UI referencing Commands.
	Ribbon    RibbonConfig `yaml:"ribbon"`
```

- [ ] **Step 4: Add validation in `Validate()`**

Insert before the final `return nil` in `Validate` (config.go:325):

```go
	fnNames := make(map[string]bool)
	for _, fn := range config.Functions {
		fnNames[fn.Name] = true
	}
	cmdNames := make(map[string]bool)
	for _, cmd := range config.Commands {
		if cmd.Name == "" {
			return fmt.Errorf("command name cannot be empty")
		}
		if fnNames[cmd.Name] {
			return fmt.Errorf("command '%s' collides with a function of the same name (xlfRegister namespace is shared)", cmd.Name)
		}
		if cmdNames[cmd.Name] {
			return fmt.Errorf("duplicate command name: %s", cmd.Name)
		}
		cmdNames[cmd.Name] = true
		if cmd.Shortcut != "" {
			r := []rune(cmd.Shortcut)
			if len(r) != 1 || !((r[0] >= 'a' && r[0] <= 'z') || (r[0] >= 'A' && r[0] <= 'Z')) {
				return fmt.Errorf("command '%s': shortcut must be a single letter (Excel binds it as Ctrl+Shift+<letter>), got %q", cmd.Name, cmd.Shortcut)
			}
		}
	}

	if config.Ribbon.Enabled() {
		if len(config.Commands) == 0 {
			return fmt.Errorf("ribbon requires at least one entry in 'commands'")
		}
		if config.Ribbon.XML != "" && (config.Ribbon.Tab != "" || len(config.Ribbon.Groups) > 0) {
			return fmt.Errorf("ribbon: 'xml' and 'tab'/'groups' are mutually exclusive")
		}
		for _, g := range config.Ribbon.Groups {
			for _, btn := range g.Buttons {
				if !cmdNames[btn.Command] {
					return fmt.Errorf("ribbon button '%s': unknown command '%s'", btn.Label, btn.Command)
				}
				switch btn.Size {
				case "", "normal", "large":
					// ok
				default:
					return fmt.Errorf("ribbon button '%s': invalid size '%s' (allowed: normal, large)", btn.Label, btn.Size)
				}
			}
		}
	}
```

(Raw-XML `onAction` cross-checking needs file IO and lives in `internal/ribbon` — Task 2 — because `Validate` has no base directory.)

- [ ] **Step 5: Add defaults in `ApplyDefaults()`**

Insert at the end of `ApplyDefaults` (config.go:410), mirroring the RTD block:

```go
	for i := range config.Commands {
		if config.Commands[i].Handler == "" {
			config.Commands[i].Handler = config.Commands[i].Name
		}
	}

	if config.Ribbon.Enabled() {
		if config.Ribbon.ProgID == "" {
			config.Ribbon.ProgID = config.Project.Name + ".Ribbon"
		}
		if config.Ribbon.Clsid == "" {
			u := uuid.NewSHA1(uuid.NameSpaceDNS, []byte(config.Ribbon.ProgID))
			config.Ribbon.Clsid = "{" + u.String() + "}"
		}
		for gi := range config.Ribbon.Groups {
			for bi := range config.Ribbon.Groups[gi].Buttons {
				if config.Ribbon.Groups[gi].Buttons[bi].Size == "" {
					config.Ribbon.Groups[gi].Buttons[bi].Size = "normal"
				}
			}
		}
	}
```

- [ ] **Step 6: Run tests, verify pass**

Run: `go test ./internal/config/`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/config/
git commit -m "feat(config): commands and ribbon sections in xll.yaml"
```

---

### Task 2: `internal/ribbon` — XML generation + raw-XML validation

**Files:**
- Create: `internal/ribbon/ribbon.go`
- Test: `internal/ribbon/ribbon_test.go`

- [ ] **Step 1: Write failing tests**

Create `internal/ribbon/ribbon_test.go`:

```go
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
```

- [ ] **Step 2: Run tests, verify compile failure**

Run: `go test ./internal/ribbon/`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement `internal/ribbon/ribbon.go`**

```go
// Package ribbon generates and validates Office customUI ribbon XML for
// xll-gen projects. Structured mode (tab/groups in xll.yaml) generates the
// XML; raw mode validates a user-authored customUI file against the
// declared commands.
package ribbon

import (
	"encoding/xml"
	"fmt"
	"os"
	"strings"

	"github.com/xll-gen/xll-gen/internal/config"
)

// CustomUINamespace is the Office 2007 baseline customUI namespace; every
// supported Excel build accepts it. (The 2009/07 namespace adds backstage
// support we do not use.)
const CustomUINamespace = "http://schemas.microsoft.com/office/2006/01/customui"

// dynamicCallbackAttrs are RibbonX callback attributes whose signatures are
// not onAction-shaped; v1 only dispatches onAction, so their presence in a
// raw XML file is a build error rather than a silent runtime no-op.
var dynamicCallbackAttrs = []string{
	"getLabel", "getEnabled", "getVisible", "getImage", "getScreentip",
	"getSupertip", "getSize", "getKeytip", "getPressed", "getText",
	"onChange", "getItemCount", "getItemLabel", "getItemID", "getSelectedItemID",
	"getSelectedItemIndex", "loadImage", "onLoad",
}

func escape(s string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		// strings.Builder never errors; keep the value on the impossible path.
		return s
	}
	return b.String()
}

// GenerateXML renders structured-mode ribbon config into customUI XML.
// Control ids are deterministic (xllgen_btn_<group>_<button>) and flow back
// to Go handlers as CommandContext.ControlID.
func GenerateXML(cfg *config.Config) (string, error) {
	r := cfg.Ribbon
	if r.XML != "" {
		return "", fmt.Errorf("GenerateXML called in raw-xml mode")
	}
	if r.Tab == "" {
		return "", fmt.Errorf("ribbon.tab is empty")
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<customUI xmlns="%s"><ribbon><tabs><tab id="xllgen_tab" label="%s">`,
		CustomUINamespace, escape(r.Tab))
	for gi, g := range r.Groups {
		fmt.Fprintf(&b, `<group id="xllgen_grp_%d" label="%s">`, gi, escape(g.Label))
		for bi, btn := range g.Buttons {
			fmt.Fprintf(&b, `<button id="xllgen_btn_%d_%d" label="%s" size="%s" onAction="%s"`,
				gi, bi, escape(btn.Label), escape(btn.Size), escape(btn.Command))
			if btn.Image != "" {
				fmt.Fprintf(&b, ` imageMso="%s"`, escape(btn.Image))
			}
			b.WriteString(`/>`)
		}
		b.WriteString(`</group>`)
	}
	b.WriteString(`</tab></tabs></ribbon></customUI>`)
	return b.String(), nil
}

// ValidateRawXML parses a user-authored customUI file and checks that every
// onAction references a declared command and that no unsupported (non-
// onAction-shaped) callback attributes are present. Returns the file content
// on success so the caller can embed it without a second read.
func ValidateRawXML(path string, commands []config.Command) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("ribbon.xml: %w", err)
	}
	known := make(map[string]bool, len(commands))
	for _, c := range commands {
		known[c.Name] = true
	}

	dec := xml.NewDecoder(strings.NewReader(string(raw)))
	for {
		tok, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return "", fmt.Errorf("ribbon.xml: parse error: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		for _, attr := range se.Attr {
			if attr.Name.Local == "onAction" {
				if !known[attr.Value] {
					return "", fmt.Errorf("ribbon.xml: onAction=%q on <%s> does not match any commands[].name (known: %s)",
						attr.Value, se.Name.Local, commandNames(commands))
				}
				continue
			}
			for _, dyn := range dynamicCallbackAttrs {
				if attr.Name.Local == dyn {
					return "", fmt.Errorf("ribbon.xml: callback attribute %q on <%s> is not supported in v1 (only onAction is dispatched)",
						dyn, se.Name.Local)
				}
			}
		}
	}
	return string(raw), nil
}

func commandNames(cmds []config.Command) string {
	names := make([]string, len(cmds))
	for i, c := range cmds {
		names[i] = c.Name
	}
	return strings.Join(names, ", ")
}

// ToCppRawLiteral wraps XML in a C++ wide raw string literal for embedding in
// the generated ribbon_xml.h. The delimiter is fixed; XML containing the
// closing sequence is rejected (it cannot occur in well-formed customUI XML).
func ToCppRawLiteral(xmlStr string) (string, error) {
	const delim = "XLLRIBBON"
	if strings.Contains(xmlStr, ")"+delim+`"`) {
		return "", fmt.Errorf("ribbon xml contains the raw-literal delimiter sequence )%s\"", delim)
	}
	return `LR"` + delim + `(` + xmlStr + `)` + delim + `"`, nil
}
```

- [ ] **Step 4: Run tests, verify pass**

Run: `go test ./internal/ribbon/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/ribbon/
git commit -m "feat(ribbon): customUI XML generation and raw-XML validation"
```

---

### Task 3: Protocol — fbs tables + message IDs (cross-repo)

**Files:**
- Modify: `internal/templates/protocol.fbs`
- Modify: `../types` repo: its `protocol.fbs` (same content) + regenerate Go/C++
- Modify: `internal/assets/files/include/xll_ipc.h`
- Modify: `pkg/server/types.go`

- [ ] **Step 1: Add tables to `internal/templates/protocol.fbs`**

After the `BatchRtdUpdate` table (line 137-139), append:

```flatbuffers

// Command (ribbon/macro) Messages

table CommandInvokeRequest {
  command_name: string;     // routes to the registered Go handler
  control_id: string;       // ribbon control id; empty for shortcut/Alt+F8
}

table CommandInvokeResponse {  // delivery ack, NOT handler completion
  ok: bool;
  error: string;
}
```

- [ ] **Step 2: Mirror into the types repo and regenerate**

In `C:\Users\minje\Nextcloud\devprj\xll-gen\types`: locate its protocol fbs (`Glob: **/*.fbs`), apply the identical table additions, then run that repo's generation workflow (check `Taskfile.yml` / `AGENTS.md` for the regen task, e.g. `task generate`; it must refresh both `go/protocol/` and the C++ `protocol_generated.h`). Run the types repo's tests.

Expected: new files `go/protocol/CommandInvokeRequest.go`, `CommandInvokeResponse.go`; updated C++ header.

- [ ] **Step 3: Point xll-gen at the local types during development**

```bash
cd C:\Users\minje\Nextcloud\devprj\xll-gen\xll-gen
go mod edit -replace github.com/xll-gen/types=../types
go mod tidy
```

**Release note (do NOT skip at the end):** before tagging, the types repo must be tagged, the replace dropped (`go mod edit -dropreplace github.com/xll-gen/types`), `go.mod` and `internal/versions` (`versions.Types` used by `CMakeLists.txt.tmpl` `GIT_TAG`) bumped to that tag, and cross-repo-coordinator run. This is Task 10.

- [ ] **Step 4: Message ID — C++ side**

In `internal/assets/files/include/xll_ipc.h` after `#define MSG_RTD_HEARTBEAT 136`:

```cpp
// Command (ribbon/macro) Messages (137)
#define MSG_COMMAND_INVOKE 137
```

- [ ] **Step 5: Message ID — Go mirror**

In `pkg/server/types.go` after `MsgRtdHeartbeat  = 136`:

```go
	// Command (ribbon/macro) invocation — must stay in sync with
	// MSG_COMMAND_INVOKE in internal/assets/files/include/xll_ipc.h.
	MsgCommandInvoke = 137
```

- [ ] **Step 6: Build everything**

Run: `go build ./...`
Expected: success (protocol package now has the new types).

- [ ] **Step 7: Commit (both repos)**

```bash
# types repo: commit fbs + regenerated code
# xll-gen repo:
git add internal/templates/protocol.fbs internal/assets/files/include/xll_ipc.h pkg/server/types.go go.mod go.sum
git commit -m "feat(protocol): CommandInvoke messages (MSG_COMMAND_INVOKE=137)"
```

---

### Task 4: Go runtime — `CommandContext` + `HandleCommandInvoke`

**Files:**
- Modify: `pkg/server/types.go`, `pkg/server/handlers.go`
- Test: `pkg/server/handlers_command_test.go` (create)

- [ ] **Step 1: Write failing tests**

Create `pkg/server/handlers_command_test.go`:

```go
package server

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/types/go/protocol"
)

func buildCommandInvoke(t *testing.T, name, controlID string) []byte {
	t.Helper()
	b := flatbuffers.NewBuilder(64)
	nameOff := b.CreateString(name)
	ctrlOff := b.CreateString(controlID)
	protocol.CommandInvokeRequestStart(b)
	protocol.CommandInvokeRequestAddCommandName(b, nameOff)
	protocol.CommandInvokeRequestAddControlId(b, ctrlOff)
	b.Finish(protocol.CommandInvokeRequestEnd(b))
	return b.FinishedBytes()
}

func newTestSysHandler() *SystemHandler {
	return NewSystemHandler(NewChunkManager(), NewAsyncBatcher(), NewCommandBatcher(), NewRefCache(), nil)
}

func parseInvokeResp(t *testing.T, respBuf []byte, n int32) *protocol.CommandInvokeResponse {
	t.Helper()
	if n <= 0 {
		t.Fatalf("no response written, n=%d", n)
	}
	return protocol.GetRootAsCommandInvokeResponse(respBuf[:n], 0)
}

func TestHandleCommandInvoke_RoutesAndAcks(t *testing.T) {
	h := newTestSysHandler()
	respBuf := make([]byte, 4096)
	b := flatbuffers.NewBuilder(256)

	var mu sync.Mutex
	var got CommandContext
	done := make(chan struct{})
	resolve := func(name string) (func(context.Context, CommandContext) error, bool) {
		if name != "RunReport" {
			return nil, false
		}
		return func(_ context.Context, cmd CommandContext) error {
			mu.Lock()
			got = cmd
			mu.Unlock()
			close(done)
			return nil
		}, true
	}

	data := buildCommandInvoke(t, "RunReport", "xllgen_btn_0_0")
	n, msgType := h.HandleCommandInvoke(data, respBuf, b, resolve)
	if msgType != MsgCommandInvoke {
		t.Fatalf("msgType: got %d want %d", msgType, MsgCommandInvoke)
	}
	resp := parseInvokeResp(t, respBuf, n)
	if !resp.Ok() {
		t.Fatalf("expected ok=true, error=%s", resp.Error())
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler not invoked within 2s")
	}
	mu.Lock()
	defer mu.Unlock()
	if got.CommandName != "RunReport" || got.ControlID != "xllgen_btn_0_0" {
		t.Errorf("CommandContext: %+v", got)
	}
}

func TestHandleCommandInvoke_UnknownCommand(t *testing.T) {
	h := newTestSysHandler()
	respBuf := make([]byte, 4096)
	b := flatbuffers.NewBuilder(256)
	resolve := func(string) (func(context.Context, CommandContext) error, bool) { return nil, false }

	n, _ := h.HandleCommandInvoke(buildCommandInvoke(t, "Nope", ""), respBuf, b, resolve)
	resp := parseInvokeResp(t, respBuf, n)
	if resp.Ok() {
		t.Fatal("expected ok=false for unknown command")
	}
	if !strings.Contains(string(resp.Error()), "Nope") {
		t.Errorf("error should name the command: %s", resp.Error())
	}
}

func TestHandleCommandInvoke_PanicRecovered(t *testing.T) {
	h := newTestSysHandler()
	respBuf := make([]byte, 4096)
	b := flatbuffers.NewBuilder(256)
	invoked := make(chan struct{})
	resolve := func(string) (func(context.Context, CommandContext) error, bool) {
		return func(context.Context, CommandContext) error {
			close(invoked)
			panic("boom")
		}, true
	}

	n, _ := h.HandleCommandInvoke(buildCommandInvoke(t, "X", ""), respBuf, b, resolve)
	resp := parseInvokeResp(t, respBuf, n)
	if !resp.Ok() {
		t.Fatal("delivery ack should be ok even if handler later panics")
	}
	select {
	case <-invoked:
	case <-time.After(2 * time.Second):
		t.Fatal("handler not invoked")
	}
	// Give the recover deferral a moment; the test passes if we don't crash.
	time.Sleep(50 * time.Millisecond)
}
```

- [ ] **Step 2: Run tests, verify compile failure**

Run: `go test ./pkg/server/ -run TestHandleCommandInvoke`
Expected: FAIL — `CommandContext`/`HandleCommandInvoke` undefined.

- [ ] **Step 3: Add `CommandContext` to `pkg/server/types.go`**

After the `PendingAsyncResult` type:

```go
// CommandContext carries invocation metadata to a user command handler
// (ribbon button click, keyboard shortcut, or typed macro name).
type CommandContext struct {
	// CommandName is the invoked commands[].name from xll.yaml.
	CommandName string
	// ControlID is the clicked ribbon control id ("" for shortcut/Alt+F8).
	ControlID string
	// ExcelPID is the parent Excel process id, for multi-instance COM attach.
	ExcelPID uint32
}
```

- [ ] **Step 4: Add `HandleCommandInvoke` to `pkg/server/handlers.go`**

Add `"os"` and `"runtime/debug"` to the import block, then after `HandleCalculationCanceled`:

```go
// excelParentPID is the PID of the process that spawned this server — the
// hosting Excel. Captured once at startup; handlers receive it via
// CommandContext.ExcelPID for multi-instance COM attachment.
var excelParentPID = uint32(os.Getppid())

// HandleCommandInvoke processes a ribbon/macro command invocation. The
// response is a delivery ack only — the handler runs fire-and-forget in its
// own goroutine, because the C++ side must return from onAction immediately
// (Excel's STA thread) and the handler may re-enter Excel via COM.
func (h *SystemHandler) HandleCommandInvoke(data []byte, respBuf []byte, b *flatbuffers.Builder, resolve func(name string) (func(context.Context, CommandContext) error, bool)) (int32, shm.MsgType) {
	reqObj := protocol.GetRootAsCommandInvokeRequest(data, 0)
	name := string(reqObj.CommandName())
	controlID := string(reqObj.ControlId())

	errMsg := ""
	if fn, ok := resolve(name); ok {
		cmd := CommandContext{CommandName: name, ControlID: controlID, ExcelPID: excelParentPID}
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Error("Panic in command handler", "command", name, "error", r, "stack", string(debug.Stack()))
				}
			}()
			if err := fn(context.Background(), cmd); err != nil {
				log.Error("Command handler failed", "command", name, "error", err)
			}
		}()
	} else {
		errMsg = "unknown command: " + name
		log.Error("CommandInvoke: unknown command", "name", name)
	}

	b.Reset()
	var errOff flatbuffers.UOffsetT
	if errMsg != "" {
		errOff = b.CreateString(errMsg)
	}
	protocol.CommandInvokeResponseStart(b)
	protocol.CommandInvokeResponseAddOk(b, errMsg == "")
	if errMsg != "" {
		protocol.CommandInvokeResponseAddError(b, errOff)
	}
	b.Finish(protocol.CommandInvokeResponseEnd(b))
	return SendAckOrChunk(b.FinishedBytes(), respBuf, MsgCommandInvoke, h.ChunkManager, b)
}
```

- [ ] **Step 5: Run tests, verify pass**

Run: `go test ./pkg/server/ -run TestHandleCommandInvoke -v`
Expected: 3 PASS. Then full package: `go test ./pkg/server/` — PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/server/
git commit -m "feat(server): CommandContext and fire-and-forget HandleCommandInvoke"
```

---

### Task 5: Go codegen — interface + dispatch wiring

**Files:**
- Modify: `internal/templates/interface.go.tmpl`
- Modify: `internal/templates/server.go.tmpl`
- Modify: `internal/generator/gen_go.go` (and `gen_common.go` if interface data is built there — locate the data struct that feeds `interface.go.tmpl` first)

- [ ] **Step 1: Add command methods to `interface.go.tmpl`**

After the `{{range .Events}}` block (line 16-17), add:

```
{{range .Commands}}	{{.Handler}}(ctx context.Context, cmd server.CommandContext) error
{{end}}
```

The template's import block must gain `"github.com/xll-gen/xll-gen/pkg/server"` guarded by commands (add alongside the existing imports at line 4-7):

```
{{if .Commands}}	"github.com/xll-gen/xll-gen/pkg/server"
{{end}}
```

- [ ] **Step 2: Add dispatch case to `server.go.tmpl`**

After the `case server.MsgChunk:` block (line 156-157), add:

```
{{if .Commands}}
             case server.MsgCommandInvoke:
                return sysHandler.HandleCommandInvoke(data, respBuf, builder, func(name string) (func(context.Context, server.CommandContext) error, bool) {
                    switch name {
                    {{range .Commands}}case "{{.Name}}":
                        return handler.{{.Handler}}, true
                    {{end}}default:
                        return nil, false
                    }
                })
{{end}}
```

- [ ] **Step 3: Pass `Commands` into both template data structs**

In `internal/generator/gen_go.go` (and wherever the `interface.go.tmpl` data struct is built — search for `interface.go.tmpl` under `internal/generator/`): add `Commands []config.Command` to the data struct and `Commands: cfg.Commands,` to its initialization, same shape as the existing `Events` field.

- [ ] **Step 4: Verify generation end-to-end**

Find the existing generator test fixture (`Grep: "xll.yaml" path:internal/generator` or a `testdata/` dir; also `go test ./internal/generator/` to discover golden tests). Add `commands:` + `ribbon:` to a fixture config, run the generator test, and inspect that the generated `interface.go` and `server.go` contain the new method and case. If the repo has an example project (`Glob: examples/**/xll.yaml`), run `go run . gen` there and `go build ./...` the generated module.

Expected: generated code compiles; `XllService` has the command method.

- [ ] **Step 5: Commit**

```bash
git add internal/templates/interface.go.tmpl internal/templates/server.go.tmpl internal/generator/
git commit -m "feat(codegen): command handler interface and MsgCommandInvoke dispatch"
```

---

### Task 6: C++ `RibbonAddIn` COM class

**Files:**
- Create: `internal/assets/files/include/com/extensibility.h`
- Create: `internal/assets/files/include/com/dispatch_helpers.h`
- Create: `internal/assets/files/include/com/ribbon_addin.h`
- Create: `internal/assets/files/src/ribbon_addin.cpp`

First verify how `internal/assets` embeds files (`Read: internal/assets/assets.go` or equivalent `go:embed` directive) — if the embed pattern is per-directory, add the `com/` subdirectory to it.

- [ ] **Step 1: Create `include/com/extensibility.h`**

```cpp
#pragma once
// Manual definitions of the Office extensibility interfaces so we do not
// depend on MIDL-generated headers from the Office SDK.
#include <windows.h>
#include <ole2.h>

// {B65AD801-ABAF-11D0-BB8B-00A0C90F2744}
static const IID IID_IDTExtensibility2 =
    { 0xB65AD801, 0xABAF, 0x11D0, { 0xBB, 0x8B, 0x00, 0xA0, 0xC9, 0x0F, 0x27, 0x44 } };

// {000C0396-0000-0000-C000-000000000046}
static const IID IID_IRibbonExtensibility =
    { 0x000C0396, 0x0000, 0x0000, { 0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46 } };

struct IDTExtensibility2 : public IDispatch {
    virtual HRESULT STDMETHODCALLTYPE OnConnection(IDispatch* Application, int ConnectMode, IDispatch* AddInInst, SAFEARRAY** custom) = 0;
    virtual HRESULT STDMETHODCALLTYPE OnDisconnection(int RemoveMode, SAFEARRAY** custom) = 0;
    virtual HRESULT STDMETHODCALLTYPE OnAddInsUpdate(SAFEARRAY** custom) = 0;
    virtual HRESULT STDMETHODCALLTYPE OnStartupComplete(SAFEARRAY** custom) = 0;
    virtual HRESULT STDMETHODCALLTYPE OnBeginShutdown(SAFEARRAY** custom) = 0;
};

struct IRibbonExtensibility : public IDispatch {
    virtual HRESULT STDMETHODCALLTYPE GetCustomUI(BSTR RibbonID, BSTR* RibbonXml) = 0;
};
```

- [ ] **Step 2: Create `include/com/dispatch_helpers.h`**

```cpp
#pragma once
// Minimal late-bound IDispatch helpers for the ribbon add-in connect path.
// Not a general automation layer — that is sugar's job on the Go side.
#include <windows.h>
#include <ole2.h>
#include <string>
#include <vector>

namespace xll { namespace com {

    inline HRESULT GetDispId(IDispatch* disp, const wchar_t* name, DISPID* out) {
        if (!disp) return E_POINTER;
        LPOLESTR n = const_cast<LPOLESTR>(name);
        return disp->GetIDsOfNames(IID_NULL, &n, 1, LOCALE_USER_DEFAULT, out);
    }

    // Invoke flags: DISPATCH_PROPERTYGET, DISPATCH_PROPERTYPUT or DISPATCH_METHOD.
    // Args are passed in natural order and reversed internally per IDispatch ABI.
    inline HRESULT Invoke(IDispatch* disp, const wchar_t* name, WORD flags,
                          std::vector<VARIANT> args, VARIANT* result) {
        DISPID dispid;
        HRESULT hr = GetDispId(disp, name, &dispid);
        if (FAILED(hr)) return hr;

        std::vector<VARIANT> reversed(args.rbegin(), args.rend());
        DISPPARAMS dp{};
        dp.cArgs = static_cast<UINT>(reversed.size());
        dp.rgvarg = reversed.empty() ? nullptr : reversed.data();

        DISPID putid = DISPID_PROPERTYPUT;
        if (flags & DISPATCH_PROPERTYPUT) {
            dp.cNamedArgs = 1;
            dp.rgdispidNamedArgs = &putid;
        }
        return disp->Invoke(dispid, IID_NULL, LOCALE_USER_DEFAULT, flags, &dp, result, nullptr, nullptr);
    }

    inline HRESULT GetProperty(IDispatch* disp, const wchar_t* name, VARIANT* result) {
        return Invoke(disp, name, DISPATCH_PROPERTYGET, {}, result);
    }

}} // namespace xll::com
```

- [ ] **Step 3: Create `include/com/ribbon_addin.h`**

```cpp
#pragma once
#ifdef XLL_RIBBON_ENABLED
#include <windows.h>
#include <ole2.h>
#include <string>
#include <vector>
#include "com/extensibility.h"

// RibbonAddIn is the COM add-in helper class hosted by the XLL itself.
// Excel loads it through DllGetClassObject (in-memory class object first via
// CoRegisterClassObject), QIs IRibbonExtensibility for GetCustomUI, and
// delivers onAction callbacks through IDispatch::Invoke.
class RibbonAddIn : public IDTExtensibility2, public IRibbonExtensibility {
    long m_refCount;
public:
    RibbonAddIn();
    virtual ~RibbonAddIn();

    // IUnknown
    HRESULT __stdcall QueryInterface(REFIID riid, void** ppv) override;
    ULONG __stdcall AddRef() override;
    ULONG __stdcall Release() override;

    // IDispatch — only ribbon callbacks are late-bound; extensibility methods
    // are reached via vtable.
    HRESULT __stdcall GetTypeInfoCount(UINT* pctinfo) override;
    HRESULT __stdcall GetTypeInfo(UINT, LCID, ITypeInfo**) override;
    HRESULT __stdcall GetIDsOfNames(REFIID, LPOLESTR* rgszNames, UINT cNames, LCID, DISPID* rgDispId) override;
    HRESULT __stdcall Invoke(DISPID dispIdMember, REFIID, LCID, WORD, DISPPARAMS* pDispParams,
                             VARIANT*, EXCEPINFO*, UINT*) override;

    // IDTExtensibility2
    HRESULT __stdcall OnConnection(IDispatch* Application, int ConnectMode, IDispatch* AddInInst, SAFEARRAY** custom) override;
    HRESULT __stdcall OnDisconnection(int RemoveMode, SAFEARRAY** custom) override;
    HRESULT __stdcall OnAddInsUpdate(SAFEARRAY** custom) override;
    HRESULT __stdcall OnStartupComplete(SAFEARRAY** custom) override;
    HRESULT __stdcall OnBeginShutdown(SAFEARRAY** custom) override;

    // IRibbonExtensibility
    HRESULT __stdcall GetCustomUI(BSTR RibbonID, BSTR* RibbonXml) override;
};

namespace xll { namespace ribbon {
    // Both set once from xlAutoOpen (generated code) before the add-in connects.
    void SetRibbonXml(const wchar_t* xml);
    void SetCommands(std::vector<std::wstring> commandNames);

    // Fire-and-forget dispatch to the Go server (MSG_COMMAND_INVOKE).
    // Returns immediately; never blocks Excel's STA thread on the handler.
    void SendCommandInvoke(const std::string& commandNameUtf8, const std::string& controlIdUtf8);
}} // namespace xll::ribbon

#endif // XLL_RIBBON_ENABLED
```

- [ ] **Step 4: Create `src/ribbon_addin.cpp`**

Follow the fire-and-forget pattern of `RtdServer::ConnectData` (`src/xll_rtd.cpp:151-177`) exactly — detached thread, `RtdConnectInFlightGuard`-equivalent drain guard, `xll::g_isUnloading` re-checks at every yield point. Reuse `rtd::GlobalModule::Lock/Unlock` for `DllCanUnloadNow` accounting.

**Guard heads-up:** the file-wide `#ifdef XLL_RIBBON_ENABLED` written below is intentionally relaxed in Task 8 Step 1 — the `xll::ribbon` namespace (SendCommandInvoke/WaitForCommandDrain/setters) moves OUTSIDE the guard so commands work without a ribbon; only the `RibbonAddIn` class stays guarded. If executing tasks out of order, read Task 8 Step 1 first.

```cpp
#ifdef XLL_RIBBON_ENABLED

#include "com/ribbon_addin.h"
#include "com/dispatch_helpers.h"
#include "rtd/module.h"
#include "xll_log.h"
#include "xll_ipc.h"
#include "xll_lifecycle.h"
#include "types/utility.h"
#include "SHMAllocator.h"
#include "shm/DirectHost.h"
#include "shm/IPCUtils.h"
#include "types/protocol_generated.h"
#include <atomic>
#include <thread>
#include <chrono>

extern shm::DirectHost* g_phost;

namespace {
    // Set once during xlAutoOpen, read-only afterwards (no lock needed: both
    // setters run before the COM add-in is connected).
    std::wstring g_ribbonXml;
    std::vector<std::wstring> g_commandNames;

    // Ribbon callback DISPIDs start here; DISPID -> g_commandNames[dispid - kDispIdBase].
    constexpr DISPID kDispIdBase = 1000;

    std::atomic<int> g_commandInFlight{0};

    struct CommandInFlightGuard {
        CommandInFlightGuard() noexcept { g_commandInFlight.fetch_add(1, std::memory_order_acq_rel); }
        ~CommandInFlightGuard() noexcept { g_commandInFlight.fetch_sub(1, std::memory_order_acq_rel); }
        CommandInFlightGuard(const CommandInFlightGuard&) = delete;
        CommandInFlightGuard& operator=(const CommandInFlightGuard&) = delete;
    };
}

namespace xll { namespace ribbon {

    void SetRibbonXml(const wchar_t* xml) { g_ribbonXml = xml ? xml : L""; }
    void SetCommands(std::vector<std::wstring> commandNames) { g_commandNames = std::move(commandNames); }

    bool WaitForCommandDrain(unsigned int timeoutMs) {
        using clock = std::chrono::steady_clock;
        auto deadline = clock::now() + std::chrono::milliseconds(timeoutMs);
        while (g_commandInFlight.load(std::memory_order_acquire) > 0) {
            if (clock::now() >= deadline) return false;
            std::this_thread::sleep_for(std::chrono::milliseconds(1));
        }
        return true;
    }

    void SendCommandInvoke(const std::string& commandNameUtf8, const std::string& controlIdUtf8) {
        std::thread([commandNameUtf8, controlIdUtf8]() {
            CommandInFlightGuard inflight;
            if (xll::g_isUnloading.load(std::memory_order_acquire)) return;
            if (!g_phost) return;

            auto slot = g_phost->GetZeroCopySlot();
            if (xll::g_isUnloading.load(std::memory_order_acquire)) return;

            SHMAllocator allocator(slot.GetReqBuffer(), slot.GetMaxReqSize());
            flatbuffers::FlatBufferBuilder builder(slot.GetMaxReqSize(), &allocator, false);
            auto nameOff = builder.CreateString(commandNameUtf8);
            auto ctrlOff = builder.CreateString(controlIdUtf8);
            protocol::CommandInvokeRequestBuilder rb(builder);
            rb.add_command_name(nameOff);
            rb.add_control_id(ctrlOff);
            builder.Finish(rb.Finish());

            if (xll::g_isUnloading.load(std::memory_order_acquire)) return;
            slot.Send(-((int)builder.GetSize()), (shm::MsgType)MSG_COMMAND_INVOKE, 5000);
        }).detach();
    }

}} // namespace xll::ribbon

// --- RibbonAddIn ---

RibbonAddIn::RibbonAddIn() : m_refCount(1) { rtd::GlobalModule::Lock(); }
RibbonAddIn::~RibbonAddIn() { rtd::GlobalModule::Unlock(); }

HRESULT __stdcall RibbonAddIn::QueryInterface(REFIID riid, void** ppv) {
    if (!ppv) return E_POINTER;
    *ppv = nullptr;
    if (IsEqualGUID(riid, IID_IUnknown) || IsEqualGUID(riid, IID_IDispatch) ||
        IsEqualGUID(riid, IID_IDTExtensibility2)) {
        *ppv = static_cast<IDTExtensibility2*>(this);
    } else if (IsEqualGUID(riid, IID_IRibbonExtensibility)) {
        *ppv = static_cast<IRibbonExtensibility*>(this);
    } else {
        return E_NOINTERFACE;
    }
    AddRef();
    return S_OK;
}

ULONG __stdcall RibbonAddIn::AddRef() { return InterlockedIncrement(&m_refCount); }
ULONG __stdcall RibbonAddIn::Release() {
    ULONG res = InterlockedDecrement(&m_refCount);
    if (res == 0) delete this;
    return res;
}

HRESULT __stdcall RibbonAddIn::GetTypeInfoCount(UINT* pctinfo) { if (pctinfo) *pctinfo = 0; return S_OK; }
HRESULT __stdcall RibbonAddIn::GetTypeInfo(UINT, LCID, ITypeInfo** ppTInfo) {
    if (ppTInfo) *ppTInfo = nullptr;
    return E_NOTIMPL;
}

HRESULT __stdcall RibbonAddIn::GetIDsOfNames(REFIID, LPOLESTR* rgszNames, UINT cNames, LCID, DISPID* rgDispId) {
    if (cNames != 1 || !rgszNames || !rgDispId) return E_INVALIDARG;
    for (size_t i = 0; i < g_commandNames.size(); ++i) {
        if (_wcsicmp(rgszNames[0], g_commandNames[i].c_str()) == 0) {
            rgDispId[0] = kDispIdBase + static_cast<DISPID>(i);
            return S_OK;
        }
    }
    rgDispId[0] = DISPID_UNKNOWN;
    return DISP_E_UNKNOWNNAME;
}

HRESULT __stdcall RibbonAddIn::Invoke(DISPID dispIdMember, REFIID, LCID, WORD, DISPPARAMS* pDispParams,
                                      VARIANT*, EXCEPINFO*, UINT*) {
    size_t idx = static_cast<size_t>(dispIdMember - kDispIdBase);
    if (dispIdMember < kDispIdBase || idx >= g_commandNames.size()) return DISP_E_MEMBERNOTFOUND;

    // onAction(IRibbonControl* control): the control arrives as VT_DISPATCH.
    std::string controlId;
    if (pDispParams && pDispParams->cArgs >= 1) {
        VARIANT& v = pDispParams->rgvarg[pDispParams->cArgs - 1]; // args reversed
        if (v.vt == VT_DISPATCH && v.pdispVal) {
            VARIANT idVar; VariantInit(&idVar);
            if (SUCCEEDED(xll::com::GetProperty(v.pdispVal, L"Id", &idVar)) && idVar.vt == VT_BSTR && idVar.bstrVal) {
                controlId = WideToUtf8(std::wstring(idVar.bstrVal, SysStringLen(idVar.bstrVal)));
            }
            VariantClear(&idVar);
        }
    }

    xll::ribbon::SendCommandInvoke(WideToUtf8(g_commandNames[idx]), controlId);
    return S_OK; // returns immediately — never wait for the Go handler (STA deadlock)
}

HRESULT __stdcall RibbonAddIn::OnConnection(IDispatch*, int, IDispatch*, SAFEARRAY**) { return S_OK; }
HRESULT __stdcall RibbonAddIn::OnDisconnection(int, SAFEARRAY**) { return S_OK; }
HRESULT __stdcall RibbonAddIn::OnAddInsUpdate(SAFEARRAY**) { return S_OK; }
HRESULT __stdcall RibbonAddIn::OnStartupComplete(SAFEARRAY**) { return S_OK; }
HRESULT __stdcall RibbonAddIn::OnBeginShutdown(SAFEARRAY**) { return S_OK; }

HRESULT __stdcall RibbonAddIn::GetCustomUI(BSTR RibbonID, BSTR* RibbonXml) {
    (void)RibbonID; // only the workbook ribbon is supported
    if (!RibbonXml) return E_POINTER;
    if (g_ribbonXml.empty()) return E_FAIL;
    *RibbonXml = SysAllocString(g_ribbonXml.c_str());
    return *RibbonXml ? S_OK : E_OUTOFMEMORY;
}

#endif // XLL_RIBBON_ENABLED
```

Also declare the drain hook in `ribbon_addin.h` inside the `xll::ribbon` namespace (used by Task 7's shutdown path):

```cpp
    // Drains in-flight SendCommandInvoke threads; mirrors WaitForRtdConnectDrain.
    bool WaitForCommandDrain(unsigned int timeoutMs);
```

- [ ] **Step 5: Verify the assets embed picks up `com/`**

Run: `go build ./...` then `Grep: "embed" path:internal/assets` — confirm the `go:embed` pattern covers `files/include/com/*` and the new src file (e.g. pattern `files/**` already recursive). If not, extend the embed directive.

- [ ] **Step 6: Commit**

```bash
git add internal/assets/files/include/com/ internal/assets/files/src/ribbon_addin.cpp
git commit -m "feat(cpp): RibbonAddIn COM add-in helper with fire-and-forget command dispatch"
```

---

### Task 7: C++ integration — registry, xlAutoOpen/xlAutoClose, codegen data, CMake

**Files:**
- Modify: `internal/assets/files/include/rtd/registry.h`
- Modify: `internal/templates/xll_main.cpp.tmpl`
- Modify: `internal/generator/gen_cpp.go`
- Modify: `internal/generator/generator.go` (emit `ribbon_xml.h`)
- Modify: `internal/templates/CMakeLists.txt.tmpl`

- [ ] **Step 1: Office Addins registry helpers in `rtd/registry.h`**

Append inside `namespace rtd` (before the closing brace):

```cpp
    /**
     * @brief Registers the COM add-in under Excel's HKCU Addins key so
     * Application.COMAddIns can see it. LoadBehavior=0: we connect
     * programmatically from xlAutoOpen, never on Excel startup.
     */
    inline HRESULT RegisterOfficeAddinKey(const wchar_t* progID, const wchar_t* friendlyName, const wchar_t* description) {
        if (!progID || !*progID) return E_INVALIDARG;
        std::wstring key = L"Software\\Microsoft\\Office\\Excel\\Addins\\";
        key += progID;

        HKEY hKey;
        if (RegCreateKeyExW(HKEY_CURRENT_USER, key.c_str(), 0, nullptr, REG_OPTION_NON_VOLATILE, KEY_SET_VALUE, nullptr, &hKey, nullptr) != ERROR_SUCCESS)
            return E_FAIL;

        DWORD loadBehavior = 0;
        RegSetValueExW(hKey, L"LoadBehavior", 0, REG_DWORD, (const BYTE*)&loadBehavior, sizeof(loadBehavior));
        if (friendlyName)
            RegSetValueExW(hKey, L"FriendlyName", 0, REG_SZ, (const BYTE*)friendlyName, (wcslen(friendlyName) + 1) * sizeof(wchar_t));
        if (description)
            RegSetValueExW(hKey, L"Description", 0, REG_SZ, (const BYTE*)description, (wcslen(description) + 1) * sizeof(wchar_t));
        RegCloseKey(hKey);
        return S_OK;
    }

    inline HRESULT UnregisterOfficeAddinKey(const wchar_t* progID) {
        if (!progID || !*progID) return E_INVALIDARG;
        std::wstring key = L"Software\\Microsoft\\Office\\Excel\\Addins\\";
        key += progID;
        RegDeleteTreeW(HKEY_CURRENT_USER, key.c_str());
        return S_OK;
    }
```

- [ ] **Step 2: Generator emits `ribbon_xml.h`**

In `internal/generator/gen_cpp.go` add:

```go
// generateRibbonXmlHeader writes the embedded customUI XML for ribbon-enabled
// projects. Structured mode generates the XML; raw mode validates the user's
// file against the declared commands and embeds it verbatim.
func generateRibbonXmlHeader(cfg *config.Config, dir string, baseDir string) error {
	if !cfg.Ribbon.Enabled() {
		return nil
	}
	var xmlStr string
	var err error
	if cfg.Ribbon.XML != "" {
		xmlStr, err = ribbon.ValidateRawXML(filepath.Join(baseDir, cfg.Ribbon.XML), cfg.Commands)
	} else {
		xmlStr, err = ribbon.GenerateXML(cfg)
	}
	if err != nil {
		return err
	}
	lit, err := ribbon.ToCppRawLiteral(xmlStr)
	if err != nil {
		return err
	}
	content := "// Code generated by xll-gen. DO NOT EDIT.\n#pragma once\n" +
		"inline const wchar_t* kXllRibbonXml = " + lit + ";\n"
	return os.WriteFile(filepath.Join(dir, "ribbon_xml.h"), []byte(content), 0o644)
}
```

(Imports: add `"os"`, `"github.com/xll-gen/xll-gen/internal/ribbon"`.) Wire the call into `internal/generator/generator.go`'s `Generate` next to `generateCppMain`, passing the project base dir (where `xll.yaml` lives). Add `Ribbon config.RibbonConfig` and `Commands []config.Command` fields to BOTH data structs in `gen_cpp.go` (`generateCppMain` and `generateCMake`), initialized from `cfg`.

- [ ] **Step 3: `xll_main.cpp.tmpl` — includes and CLSID**

After the RTD include block (line 34-39), add:

```
{{if .Ribbon.Enabled}}
#define XLL_RIBBON_ENABLED
#include "com/ribbon_addin.h"
#include "com/dispatch_helpers.h"
#include "rtd/factory.h"
#include "rtd/module.h"
#include "rtd/registry.h"
#include "ribbon_xml.h"
#include <oleacc.h>
{{end}}
```

**Important:** `XLL_RIBBON_ENABLED` must also be defined for `ribbon_addin.cpp`'s compilation — that happens via CMake (Step 6), not this header-local define; keep both.

After the RTD CLSID block (line 57-71), add:

```
{{if .Ribbon.Enabled}}
const CLSID& GetRibbonClsid() {
    static CLSID clsid = []{
        CLSID c{};
        CLSIDFromString(L"{{.Ribbon.Clsid}}", &c);
        return c;
    }();
    return clsid;
}
const wchar_t* g_szRibbonProgID = L"{{.Ribbon.ProgID}}";
DWORD g_ribbonCookie = 0;
{{end}}
```

- [ ] **Step 4: `xll_main.cpp.tmpl` — DllGetClassObject / DllCanUnloadNow guards**

Change the guard at line 57 (`{{if .Rtd.Enabled}}` wrapping the `extern "C"` COM export block) so the exports exist when either feature is on. Restructure to:

```
{{if or .Rtd.Enabled .Ribbon.Enabled}}
extern "C" {
    __declspec(dllexport) HRESULT __stdcall DllGetClassObject(REFCLSID rclsid, REFIID riid, LPVOID* ppv) {
        if (!ppv) return E_POINTER;
        *ppv = nullptr;
        {{if .Rtd.Enabled}}
        if (IsEqualGUID(rclsid, GetRtdClsid())) {
            rtd::ClassFactory<RtdServer>* pFactory = new rtd::ClassFactory<RtdServer>();
            if (!pFactory) return E_OUTOFMEMORY;
            HRESULT hr = pFactory->QueryInterface(riid, ppv);
            pFactory->Release();
            return hr;
        }
        {{end}}
        {{if .Ribbon.Enabled}}
        if (IsEqualGUID(rclsid, GetRibbonClsid())) {
            rtd::ClassFactory<RibbonAddIn>* pFactory = new rtd::ClassFactory<RibbonAddIn>();
            if (!pFactory) return E_OUTOFMEMORY;
            HRESULT hr = pFactory->QueryInterface(riid, ppv);
            pFactory->Release();
            return hr;
        }
        {{end}}
        return CLASS_E_CLASSNOTAVAILABLE;
    }

    __declspec(dllexport) HRESULT __stdcall DllCanUnloadNow() {
        return (rtd::GlobalModule::GetLockCount() == 0) ? S_OK : S_FALSE;
    }
    {{if .Rtd.Enabled}}
    __declspec(dllexport) HRESULT __stdcall DllRegisterServer() {
        return rtd::RegisterServer(g_hModule, GetRtdClsid(), g_szProgID, g_szFriendlyName);
    }
    __declspec(dllexport) HRESULT __stdcall DllUnregisterServer() {
        return rtd::UnregisterServer(GetRtdClsid(), g_szProgID);
    }
    {{end}}
}
{{end}}
```

Preserve the existing debug logging lines inside `DllGetClassObject` if desired (they reference SAFE_LOG_DEBUG which is defined above this point). Keep `g_rtdCookie` declaration under `{{if .Rtd.Enabled}}` as today.

- [ ] **Step 5: `xll_main.cpp.tmpl` — xlAutoOpen ribbon bootstrap**

Insert AFTER the existing function-registration loop and event registration in `xlAutoOpen` (i.e., near the end, after SHM + server launch + registrations are done; anchor: just before xlAutoOpen's final `return 1;`):

```
    {{if .Ribbon.Enabled}}
    // ---- Ribbon COM add-in bootstrap (graceful degradation: any failure
    // logs a warning and leaves functions/commands fully operational). ----
    XLL_SAFE_BLOCK_BEGIN
        xll::ribbon::SetRibbonXml(kXllRibbonXml);
        xll::ribbon::SetCommands({ {{range .Commands}}L"{{.Name}}", {{end}} });

        bool ribbonOk = true;

        // 1. HKCU COM registration (CLSID/InprocServer32 + ProgID).
        if (FAILED(rtd::RegisterServer(g_hModule, GetRibbonClsid(), g_szRibbonProgID, L"{{.ProjectName}} Ribbon"))) {
            SAFE_LOG_WARN("Ribbon: HKCU COM registration failed (locked-down registry?); ribbon UI disabled.");
            ribbonOk = false;
        }
        // 2. Office Addins key so COMAddIns enumerates us.
        if (ribbonOk && FAILED(rtd::RegisterOfficeAddinKey(g_szRibbonProgID, L"{{.ProjectName}}", L"{{.ProjectName}} ribbon helper"))) {
            SAFE_LOG_WARN("Ribbon: Office Addins key registration failed; ribbon UI disabled.");
            ribbonOk = false;
        }
        // 3. In-memory class object so CoCreateInstance resolves without
        //    registry CLSID lookup (mirrors the RTD pattern).
        if (ribbonOk) {
            rtd::ClassFactory<RibbonAddIn>* pFactory = new rtd::ClassFactory<RibbonAddIn>();
            HRESULT hr = CoRegisterClassObject(GetRibbonClsid(), pFactory, CLSCTX_INPROC_SERVER, REGCLS_MULTIPLEUSE, &g_ribbonCookie);
            pFactory->Release();
            if (FAILED(hr)) {
                SAFE_LOG_WARN("Ribbon: CoRegisterClassObject failed: " + std::to_string(hr));
                ribbonOk = false;
            }
        }
        // 4. Connect through Application.COMAddIns. Application object comes
        //    from the in-process accessibility path: xlGetHwnd -> EXCEL7
        //    child window -> AccessibleObjectFromWindow(OBJID_NATIVEOM).
        if (ribbonOk && !ConnectRibbonAddin()) {
            SAFE_LOG_WARN("Ribbon: COMAddIns connect failed; ribbon UI disabled.");
            ribbonOk = false;
        }
        if (ribbonOk) {
            SAFE_LOG_INFO("Ribbon: COM add-in connected.");
        }
    XLL_SAFE_BLOCK_END_CONTINUE
    {{end}}
```

And add the `ConnectRibbonAddin` helper above `xlAutoOpen` in the template:

```
{{if .Ribbon.Enabled}}
// Finds the hosting Excel's Application IDispatch and connects our COM
// add-in: Application.COMAddIns.Item(progId).Connect = true.
static BOOL CALLBACK FindExcel7Proc(HWND hwnd, LPARAM lParam) {
    wchar_t cls[16] = {};
    GetClassNameW(hwnd, cls, 15);
    if (wcscmp(cls, L"EXCEL7") == 0) {
        *reinterpret_cast<HWND*>(lParam) = hwnd;
        return FALSE;
    }
    return TRUE;
}

static bool ConnectRibbonAddin() {
    // xlGetHwnd returns the LOW WORD of the main window handle in val.w —
    // recover the full HWND by matching against top-level windows is overkill
    // in-process: EnumThreadWindows on the current thread finds Excel's frame.
    ScopedXLOPER12Result xHwnd;
    if (xll::CallExcel(xlGetHwnd, xHwnd) != xlretSuccess) return false;

    HWND desk = nullptr;
    // Find the XLDESK -> EXCEL7 child under Excel's main frame. In-process,
    // GetActiveWindow()/EnumThreadWindows both work; use the documented route:
    HWND frame = nullptr;
    EnumThreadWindows(GetCurrentThreadId(), [](HWND h, LPARAM p) -> BOOL {
        wchar_t cls[32] = {};
        GetClassNameW(h, cls, 31);
        if (wcscmp(cls, L"XLMAIN") == 0) { *reinterpret_cast<HWND*>(p) = h; return FALSE; }
        return TRUE;
    }, reinterpret_cast<LPARAM>(&frame));
    if (!frame) return false;

    HWND excel7 = nullptr;
    HWND xldesk = FindWindowExW(frame, nullptr, L"XLDESK", nullptr);
    if (xldesk) excel7 = FindWindowExW(xldesk, nullptr, L"EXCEL7", nullptr);
    if (!excel7) {
        EnumChildWindows(frame, FindExcel7Proc, reinterpret_cast<LPARAM>(&excel7));
    }
    if (!excel7) return false;

    IDispatch* pWindow = nullptr;
    if (FAILED(AccessibleObjectFromWindow(excel7, OBJID_NATIVEOM, IID_IDispatch, reinterpret_cast<void**>(&pWindow))) || !pWindow)
        return false;

    bool ok = false;
    VARIANT vApp; VariantInit(&vApp);
    if (SUCCEEDED(xll::com::GetProperty(pWindow, L"Application", &vApp)) && vApp.vt == VT_DISPATCH && vApp.pdispVal) {
        VARIANT vAddins; VariantInit(&vAddins);
        if (SUCCEEDED(xll::com::GetProperty(vApp.pdispVal, L"COMAddIns", &vAddins)) && vAddins.vt == VT_DISPATCH && vAddins.pdispVal) {
            VARIANT vProg; VariantInit(&vProg);
            vProg.vt = VT_BSTR;
            vProg.bstrVal = SysAllocString(g_szRibbonProgID);
            VARIANT vItem; VariantInit(&vItem);
            if (SUCCEEDED(xll::com::Invoke(vAddins.pdispVal, L"Item", DISPATCH_METHOD | DISPATCH_PROPERTYGET, { vProg }, &vItem))
                && vItem.vt == VT_DISPATCH && vItem.pdispVal) {
                VARIANT vTrue; VariantInit(&vTrue);
                vTrue.vt = VT_BOOL;
                vTrue.boolVal = VARIANT_TRUE;
                ok = SUCCEEDED(xll::com::Invoke(vItem.pdispVal, L"Connect", DISPATCH_PROPERTYPUT, { vTrue }, nullptr));
            }
            VariantClear(&vItem);
            VariantClear(&vProg);
        }
        VariantClear(&vAddins);
    }
    VariantClear(&vApp);
    pWindow->Release();
    return ok;
}
{{end}}
```

(C++ note for the implementer: the capture-less lambda passed to `EnumThreadWindows` decays to `WNDENUMPROC`; if the compiler rejects the calling-convention conversion under `/W4`, hoist it into a named `static BOOL CALLBACK` like `FindExcel7Proc`.)

- [ ] **Step 6: `xll_main.cpp.tmpl` — xlAutoClose disconnect**

In `xlAutoClose` (line 111-126), before the RTD revoke block, add:

```
    {{if .Ribbon.Enabled}}
    XLL_SAFE_BLOCK_BEGIN
        // Best-effort: drain in-flight command sends, revoke class object,
        // remove the Addins key (re-created on next load).
        xll::ribbon::WaitForCommandDrain(2000);
        if (g_ribbonCookie != 0) {
            CoRevokeClassObject(g_ribbonCookie);
            g_ribbonCookie = 0;
        }
        rtd::UnregisterOfficeAddinKey(g_szRibbonProgID);
        rtd::UnregisterServer(GetRibbonClsid(), g_szRibbonProgID);
    XLL_SAFE_BLOCK_END_CONTINUE
    {{end}}
```

(The drain must run BEFORE `xll::OnAutoClose()` deletes `g_phost` — placing it in `xlAutoClose` ahead of the `return xll::OnAutoClose();` line satisfies this.)

- [ ] **Step 7: CMake**

In `internal/templates/CMakeLists.txt.tmpl`, locate where `xll_rtd.cpp` is conditionally added to the target (anchor: search `rtd`) and mirror for ribbon:

```cmake
{{if .Ribbon.Enabled}}
target_sources({{.ProjectName}} PRIVATE ${XLL_ASSETS_DIR}/src/ribbon_addin.cpp)
target_compile_definitions({{.ProjectName}} PRIVATE XLL_RIBBON_ENABLED)
target_link_libraries({{.ProjectName}} PRIVATE oleacc)
{{end}}
```

Adapt variable names (`XLL_ASSETS_DIR`, target name) to what the template actually uses — read it first; the snippet shows intent, the template's own conventions win.

- [ ] **Step 8: Generate + compile an example project**

Add `commands:`/`ribbon:` to an example or scratch project config, run `xll-gen` generation, then build the C++ (per repo's Taskfile, e.g. `task build` in the example). Fix compile errors.

Expected: XLL builds with ribbon enabled AND with ribbon absent (regression: a config without `ribbon:`/`commands:` generates byte-identical-to-before output except version strings).

- [ ] **Step 9: Commit**

```bash
git add internal/assets/files/include/rtd/registry.h internal/templates/xll_main.cpp.tmpl internal/templates/CMakeLists.txt.tmpl internal/generator/
git commit -m "feat(cpp): ribbon COM add-in bootstrap with graceful degradation"
```

---

### Task 8: Command procs + macroType=2 registration

**Files:**
- Modify: `internal/templates/xll_main.cpp.tmpl`

- [ ] **Step 1: Exported command procedures**

Near the generated UDF wrappers in the template (after the ribbon helper block is fine), add:

```
{{if .Commands}}
// XLL command procedures (macroType=2). Invoked via shortcut or typed name;
// ribbon clicks go through RibbonAddIn::Invoke instead. Same fire-and-forget
// dispatch — a command proc must not block Excel's UI thread on the handler.
{{range .Commands}}
extern "C" __declspec(dllexport) int __stdcall Cmd_{{.Name}}() {
    XLL_SAFE_BLOCK_BEGIN
        xll::ribbon::SendCommandInvoke("{{.Name}}", "");
    XLL_SAFE_BLOCK_END(0)
    return 1;
}
{{end}}
{{end}}
```

**Dependency note:** `SendCommandInvoke` lives in `ribbon_addin.cpp` which compiles only under `XLL_RIBBON_ENABLED`. Commands must work WITHOUT a ribbon. Fix: in Task 6's files, guard the `RibbonAddIn` class with `XLL_RIBBON_ENABLED` but move `namespace xll::ribbon { SendCommandInvoke, WaitForCommandDrain, ... }` OUT of that guard (top of `ribbon_addin.cpp`, own `#ifdef XLL_COMMANDS_ENABLED` or no guard at all), and in CMake compile `ribbon_addin.cpp` whenever `{{if .Commands}}` is true, defining `XLL_RIBBON_ENABLED` only `{{if .Ribbon.Enabled}}`. Adjust the Task 7 CMake snippet accordingly:

```cmake
{{if .Commands}}
target_sources({{.ProjectName}} PRIVATE ${XLL_ASSETS_DIR}/src/ribbon_addin.cpp)
{{if .Ribbon.Enabled}}target_compile_definitions({{.ProjectName}} PRIVATE XLL_RIBBON_ENABLED){{end}}
target_link_libraries({{.ProjectName}} PRIVATE oleacc)
{{end}}
```

And in `ribbon_addin.cpp`, change the file-wide `#ifdef XLL_RIBBON_ENABLED` to wrap ONLY the `RibbonAddIn` class implementation; the `xll::ribbon` namespace (send/drain/setters) compiles unconditionally. Mirror in `ribbon_addin.h` (class under the guard, namespace functions outside).

- [ ] **Step 2: Registration loop in xlAutoOpen**

Immediately after the existing functions registration loop (the `{{range .Functions}}` block ending around template line 333), add:

```
    {{range .Commands}}
    {
        XLOPER12 xRegId;
        int regRes = xll::RegisterFunction(
            *xDLL,
            L"Cmd_{{.Name}}",          // Procedure (exported symbol)
            L"A",                       // TypeText: command returning short
            L"{{.Name}}",              // Name typed in the Alt+F8 dialog
            L"",                        // ArgumentText: commands take none
            2,                          // MacroType 2 = command
            L"",                        // Category (unused for commands)
            L"{{.Shortcut}}",          // Shortcut letter -> Ctrl+Shift+<letter>
            L"",                        // HelpTopic
            L"{{.Description}}",
            {},
            xRegId
        );
        if (regRes != xlretSuccess) {
            SAFE_LOG_ERROR("Failed to register command {{.Name}}: " + std::to_string(regRes));
        }
    }
    {{end}}
```

(Match the exact call shape of the existing function-registration block in the template — same `xRegId` handling and logging style; if the existing loop frees/tracks `xRegId`, do the same.)

- [ ] **Step 3: Rebuild example, verify registration**

Rebuild the Task 7 example. In the generated `xll_main.cpp`, confirm: `Cmd_RunReport` exported, registered with macroType literal `2`, functions still `1`.

Run: `go test ./...` (whole repo) — PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/templates/xll_main.cpp.tmpl internal/templates/CMakeLists.txt.tmpl internal/assets/files/
git commit -m "feat(cpp): register commands with macroType=2 and shortcut binding"
```

---

### Task 9: Regression test + E2E example

**Files:**
- Modify: `internal/templates/regtest_main.cpp.tmpl` and/or `internal/regtest/generator.go` (read both first; follow their existing assertion pattern)
- Modify/Create: example project config

- [ ] **Step 1: Read the regtest machinery**

Read `internal/regtest/generator.go` and `internal/templates/regtest_main.cpp.tmpl` to learn how registrations are asserted today.

- [ ] **Step 2: Add command assertions**

Following the discovered pattern, add a case generated `{{range .Commands}}` asserting that (a) `Cmd_<Name>` is exported from the XLL (GetProcAddress != NULL) and (b) the registration call used macroType 2 (if the regtest framework replays registrations, assert the literal; if it only checks exports, (a) suffices — document which in the commit message).

- [ ] **Step 3: E2E manual script (documented, executed once on a real Excel box)**

Append to the example project's README (or create `docs/ribbon-e2e.md`):

```markdown
## Ribbon E2E checklist
1. Build example with ribbon enabled; open the .xll in Excel.
2. Custom tab appears with the declared buttons.
3. Click button -> Go handler runs (writes to A1 via sugar) -> Excel not blocked during a slow handler (add a 5s sleep; UI must stay responsive — STA fire-and-forget proof).
4. Ctrl+Shift+<letter> invokes the handler without the ribbon.
5. Alt+F8, type command name, Run -> handler runs.
6. Graceful degradation: re-run with HKCU\...\Office\Excel\Addins ACL'd read-only (or set ribbon prog_id to an invalid key path in a scratch build) -> warning logged, no dialog, worksheet functions and shortcut still work.
7. Close Excel -> no orphan server process, no leaked SHM (two-tier cleanup),
   HKCU keys removed.
```

Excel-spawning automated tests, if added, MUST follow the two-tier cleanup rule (graceful exit, then force-kill) per workspace policy.

- [ ] **Step 4: Commit**

```bash
git add internal/regtest/ internal/templates/regtest_main.cpp.tmpl docs/
git commit -m "test: command registration regression + ribbon E2E checklist"
```

---

### Task 10: Docs, backlog, cross-repo release discipline

**Files:**
- Modify: `AGENTS.md` (xll-gen)
- Modify: `../sugar/AGENTS.md`
- Modify: `docs/superpowers/specs/2026-06-11-ribbon-command-design.md`
- Modify: `go.mod` (drop replace), `internal/versions` (Types pin) — at release time

- [ ] **Step 1: xll-gen AGENTS.md co-change cluster**

Add to the co-change documentation (follow the existing cluster format):

```markdown
### Commands/Ribbon cluster
Changing any of these requires reviewing all of them:
- `internal/config/config.go` (Command/RibbonConfig) ↔ `internal/ribbon/` ↔
  `internal/templates/{interface.go.tmpl,server.go.tmpl,xll_main.cpp.tmpl,CMakeLists.txt.tmpl}`
- `MSG_COMMAND_INVOKE` in `internal/assets/files/include/xll_ipc.h` ↔
  `MsgCommandInvoke` in `pkg/server/types.go` ↔ `CommandInvokeRequest/Response`
  in protocol.fbs (both copies: internal/templates and the types repo).
- Threading contract: RibbonAddIn::Invoke and Cmd_* procs are fire-and-forget;
  NEVER make them wait on the Go handler (Excel STA deadlock — handlers may
  re-enter Excel via COM).
```

- [ ] **Step 2: sugar backlog row**

In `../sugar/AGENTS.md` §2.1 table (or its backlog section), add:

```markdown
| `App` (multi-instance) | `excel.GetApplicationByPID` | backlog | P2 | Attach to a specific running Excel by PID; consumer: xll-gen command handlers receive `CommandContext.ExcelPID`. |
```

- [ ] **Step 3: Spec touch-up**

In the spec, update the `shortcut:` example from `"Ctrl+Shift+R"` to `"R"  # bound as Ctrl+Shift+R` (xlfRegister takes a single letter) — implementation detail discovered during planning.

- [ ] **Step 4: Release-time checklist (do not tag before these)**

1. types repo: commit + tag the protocol additions.
2. xll-gen: `go mod edit -dropreplace github.com/xll-gen/types`, bump `go.mod` + `internal/versions` Types pin to the new tag.
3. Run cross-repo-coordinator agent (verifies pins, message-ID consistency).
4. Run shm-protocol-guardian (MSG id mirror check) and xll-cpp-reviewer
   (new COM class: DllMain/unload, XLOPER12 in registration, xlAutoClose ordering).
5. Run memory-safety-auditor (new detached threads + COM refcounts).
6. `/pre-release xll-gen`.

- [ ] **Step 5: Commit**

```bash
git add AGENTS.md docs/
git commit -m "docs: commands/ribbon co-change cluster and release checklist"
# sugar repo: separate commit for the backlog row
```

---

## Verification (whole-feature, after all tasks)

- [ ] `go test ./...` in xll-gen and types — PASS.
- [ ] Example project generates + C++ builds in all four matrix cells: {no commands, commands only, structured ribbon, raw-xml ribbon}.
- [ ] E2E checklist (Task 9 Step 3) executed on a real Excel machine — all 7 items.
- [ ] Review agents (opus): xll-cpp-reviewer on the C++ assets/template changes; shm-protocol-guardian on the protocol/ID changes; code-review on the Go diff.
