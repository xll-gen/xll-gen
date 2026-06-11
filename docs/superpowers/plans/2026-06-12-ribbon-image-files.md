# Ribbon Image Files (PNG/JPG) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
> **Standing user directive:** implementation AND verification subagents run with `model="opus"`.

**Goal:** `ribbon.buttons[].image` in xll.yaml accepts PNG/JPG/BMP/GIF/ICO file paths; bytes are embedded into the XLL at build time and served to Office at runtime via a `loadImage` callback returning a GDI+-decoded `IPictureDisp` (PNG alpha preserved).

**Architecture:** Go side classifies `image:` values (file vs imageMso), loads + dedupes file bytes, emits `ribbon_images.h` next to `ribbon_xml.h`, and adds `image=`/`loadImage=` attributes to the generated customUI XML. C++ side gets a new standalone TU (`ribbon_image.cpp`: bytes → IStream → Gdiplus::Bitmap → 32bpp pARGB HBITMAP → OleCreatePictureIndirect) that `RibbonAddIn::Invoke` dispatches to for the `LoadRibbonImage` callback. The regtest mock host compiles that TU directly and verifies decode end-to-end.

**Tech Stack:** Go (config/ribbon/generator + tests), C++17 (GDI+, OLE), Go templates, CMake, regtest harness (`cmd/regression_test.go`).

**Spec:** `docs/superpowers/specs/2026-06-12-ribbon-image-files-design.md`
**Repo/branch:** `xll-gen`, `feature/ribbon-image-files` (already created; spec committed).

**Key existing facts (verified):**
- `internal/ribbon/ribbon.go:46` `GenerateXML(cfg)` emits only `imageMso`; `dynamicCallbackAttrs` (line 27) rejects `loadImage` in raw mode — **keep raw-mode rejection unchanged**.
- `internal/generator/gen_cpp.go:16` `generateRibbonXmlHeader(cfg, dir, baseDir)` writes `ribbon_xml.h` into `includeDir`; called from `internal/generator/generator.go:176`.
- `internal/templates/xll_main.cpp.tmpl`: `#include "ribbon_xml.h"` at ~line 55; `xll::ribbon::SetRibbonXml(kXllRibbonXml);` at ~line 533; ribbon teardown block in `xlAutoClose` at lines 166–182.
- `internal/templates/CMakeLists.txt.tmpl:171-183`: ribbon-enabled block defines `XLL_RIBBON_ENABLED` and links `ole32 oleaut32 uuid oleacc`. `file(GLOB ... src/*.cpp)` sweeps all asset sources — new `.cpp` files compile for every project, so guard bodies with `#ifdef XLL_RIBBON_ENABLED`.
- `internal/assets/files/src/ribbon_addin.cpp`: `kDispIdBase = 1000` (commands), `kDispIdExtBase = -1005`. The whole `RibbonAddIn` class is inside `#ifdef XLL_RIBBON_ENABLED`.
- Regtest: `cmd/regression_test.go` writes `internal/regtest/testdata/{xll.yaml, mock_host.cpp→main.cpp, CMakeLists.txt}` (embedded via `internal/regtest/assets.go`) into a temp project, runs generate, builds mock_host (include dirs `../generated/cpp` and `../generated/cpp/include`), runs it against the Go server. testdata/xll.yaml currently has `commands:` but **no `ribbon:` section**. Last mock_host test is `// 14. Command invoke`; it ends with `cout << "PASSED"`.
- v0.4.1 lesson: tests must **compile and run** generated C++, not just string-check it.

---

### Task 1: config — classify `image:` values (file vs imageMso)

**Files:**
- Modify: `internal/config/config.go` (RibbonButton doc at line 224; Validate ribbon loop at ~line 458)
- Test: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing tests** (append to `config_test.go`; match the file's existing test style)

```go
func TestClassifyRibbonImage(t *testing.T) {
	cases := []struct {
		value   string
		isFile  bool
		wantErr bool
	}{
		{"HappyFace", false, false},          // classic imageMso
		{"icon.png", true, false},            // bare filename with known ext
		{"icons/refresh.png", true, false},   // forward-slash path
		{`icons\refresh.PNG`, true, false},   // backslash path, case-insensitive ext
		{"./icons/a.jpeg", true, false},      // all supported exts are files
		{"a.bmp", true, false},
		{"a.gif", true, false},
		{"a.ico", true, false},
		{"icons/refresh.svg", false, true},   // path-like + unsupported ext -> error
		{"icons/refresh", false, true},       // path-like + no ext -> error
		{"weird.xyz", false, false},          // no separator, unknown ext -> imageMso
		{"", false, false},                   // empty -> not a file, no error
	}
	for _, c := range cases {
		isFile, err := config.ClassifyRibbonImage(c.value)
		if c.wantErr {
			if err == nil {
				t.Errorf("ClassifyRibbonImage(%q): expected error, got nil", c.value)
			}
			continue
		}
		if err != nil {
			t.Errorf("ClassifyRibbonImage(%q): unexpected error: %v", c.value, err)
			continue
		}
		if isFile != c.isFile {
			t.Errorf("ClassifyRibbonImage(%q) = %v, want %v", c.value, isFile, c.isFile)
		}
	}
}

func TestValidateRibbonImageBadExtension(t *testing.T) {
	cfg := makeValidRibbonConfig() // if no such helper exists, inline a minimal valid config with one command + one ribbon button, copying the pattern of the existing ribbon Validate tests in this file
	cfg.Ribbon.Groups[0].Buttons[0].Image = "icons/refresh.svg"
	config.ApplyDefaults(cfg)
	if err := config.Validate(cfg); err == nil {
		t.Fatal("expected Validate to reject unsupported ribbon image extension")
	}
}
```

NOTE: if `config_test.go` is in package `config` (internal test), drop the `config.` qualifier. Check the existing ribbon Validate tests in that file and reuse their config-construction helper if one exists.

- [ ] **Step 2: Run, verify failure**

Run: `go test ./internal/config/ -run TestClassifyRibbonImage -v` (in `xll-gen/`)
Expected: FAIL — `undefined: ClassifyRibbonImage`

- [ ] **Step 3: Implement** in `config.go`:

Add near the `RibbonButton` type:

```go
// ribbonImageFileExts are the formats the runtime decoder (GDI+) accepts for
// ribbon button image files.
var ribbonImageFileExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".bmp": true, ".gif": true, ".ico": true,
}

// ClassifyRibbonImage reports how a ribbon button image value is interpreted:
// a file path (embedded into the XLL, served via loadImage) or a built-in
// imageMso name. A path-like value (contains / or \) with an unsupported
// extension is an error; imageMso names never contain separators or dots.
func ClassifyRibbonImage(value string) (isFile bool, err error) {
	if value == "" {
		return false, nil
	}
	// Extension after the last dot of the last path segment, either separator.
	base := value
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	ext := strings.ToLower(path.Ext(base))
	if ribbonImageFileExts[ext] {
		return true, nil
	}
	if strings.ContainsAny(value, `/\`) {
		return false, fmt.Errorf("ribbon image %q looks like a file path but has an unsupported extension (supported: .png .jpg .jpeg .bmp .gif .ico)", value)
	}
	return false, nil
}
```

(Imports: `path` and `strings`; `fmt` is already imported.)

Update the `Image` field doc (line 224-225):

```go
	// Image is an imageMso name (e.g. "HappyFace") or a path to a
	// PNG/JPG/JPEG/BMP/GIF/ICO file relative to xll.yaml. File images are
	// embedded into the XLL and served via the loadImage ribbon callback.
	Image string `yaml:"image"`
```

In `Validate`'s ribbon button loop (after the unknown-command check at ~line 463-465):

```go
				if btn.Image != "" {
					if _, err := ClassifyRibbonImage(btn.Image); err != nil {
						return fmt.Errorf("ribbon button '%s': %w", btn.Label, err)
					}
				}
```

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/config/ -v`
Expected: PASS (all, including pre-existing tests)

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): classify ribbon image values as file path or imageMso"
```

---

### Task 2: ribbon — load/dedup image files; emit image= + loadImage= XML

**Files:**
- Modify: `internal/ribbon/ribbon.go` (GenerateXML signature change)
- Modify: all `GenerateXML` callers (`internal/generator/gen_cpp.go:25`, existing tests)
- Test: `internal/ribbon/ribbon_test.go`

- [ ] **Step 1: Write the failing tests** (append to `ribbon_test.go`; the test helper below writes a real PNG with stdlib so no binary fixture is needed)

```go
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
```

(New test imports: `image`, `image/color`, `image/png`, `os`, `path/filepath`, `strings`.)

- [ ] **Step 2: Run, verify failure**

Run: `go test ./internal/ribbon/ -v`
Expected: FAIL — `undefined: Images`; signature mismatch on `GenerateXML`.

- [ ] **Step 3: Implement** in `ribbon.go`:

Add (new imports: `os` is present; add `path/filepath`):

```go
// Image is one deduplicated ribbon image file scheduled for embedding into
// the generated ribbon_images.h.
type Image struct {
	Name string // deterministic id referenced from customUI XML (xllgen_img_<i>)
	Path string // baseDir-joined cleaned path, for error messages
	Data []byte
}

// maxImageBytes caps each embedded ribbon icon; the bytes are compiled into
// the XLL, and ribbon icons are 16x16/32x32.
const maxImageBytes = 1 << 20

// Images loads and dedupes (by cleaned path) every file-image button in
// structured mode. The second return value maps each raw yaml image value to
// its embedded image name, for GenerateXML.
func Images(cfg *config.Config, baseDir string) ([]Image, map[string]string, error) {
	var imgs []Image
	byPath := map[string]int{}
	nameByValue := map[string]string{}
	for _, g := range cfg.Ribbon.Groups {
		for _, btn := range g.Buttons {
			if btn.Image == "" {
				continue
			}
			isFile, err := config.ClassifyRibbonImage(btn.Image)
			if err != nil {
				return nil, nil, fmt.Errorf("ribbon button '%s': %w", btn.Label, err)
			}
			if !isFile {
				continue
			}
			if _, seen := nameByValue[btn.Image]; seen {
				continue
			}
			p := filepath.Clean(filepath.Join(baseDir, filepath.FromSlash(btn.Image)))
			if i, ok := byPath[p]; ok {
				nameByValue[btn.Image] = imgs[i].Name
				continue
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return nil, nil, fmt.Errorf("ribbon button '%s': image file: %w", btn.Label, err)
			}
			if len(data) == 0 {
				return nil, nil, fmt.Errorf("ribbon button '%s': image file %s is empty", btn.Label, p)
			}
			if len(data) > maxImageBytes {
				return nil, nil, fmt.Errorf("ribbon button '%s': image file %s is %d bytes (max %d); ribbon icons should be 16x16 or 32x32", btn.Label, p, len(data), maxImageBytes)
			}
			img := Image{Name: fmt.Sprintf("xllgen_img_%d", len(imgs)), Path: p, Data: data}
			byPath[p] = len(imgs)
			imgs = append(imgs, img)
			nameByValue[btn.Image] = img.Name
		}
	}
	return imgs, nameByValue, nil
}
```

Change `GenerateXML` signature and emission (`imageNames` maps raw yaml image values to embedded names; nil/empty means no file images):

```go
func GenerateXML(cfg *config.Config, imageNames map[string]string) (string, error) {
```

Replace the `<customUI ...>` opening write:

```go
	loadImageAttr := ""
	if len(imageNames) > 0 {
		loadImageAttr = ` loadImage="LoadRibbonImage"`
	}
	fmt.Fprintf(&b, `<customUI xmlns="%s"%s><ribbon><tabs><tab id="xllgen_tab" label="%s">`,
		CustomUINamespace, loadImageAttr, escape(r.Tab))
```

Replace the button image emission (lines 69-71):

```go
			if btn.Image != "" {
				if name, ok := imageNames[btn.Image]; ok {
					fmt.Fprintf(&b, ` image="%s"`, escape(name))
				} else {
					fmt.Fprintf(&b, ` imageMso="%s"`, escape(btn.Image))
				}
			}
```

Update the `GenerateXML` doc comment to mention the map. Fix every existing caller: in existing `ribbon_test.go` tests pass `nil`; `gen_cpp.go` is rewritten in Task 3 (for now make it compile by passing `nil` — Task 3 finishes the wiring).

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/ribbon/ ./internal/generator/ ./internal/config/ -v` and `go build ./...`
Expected: PASS / clean build

- [ ] **Step 5: Commit**

```bash
git add internal/ribbon/ribbon.go internal/ribbon/ribbon_test.go internal/generator/gen_cpp.go
git commit -m "feat(ribbon): load/dedupe image files and emit image= + loadImage= customUI attributes"
```

---

### Task 3: generator — emit ribbon_images.h and wire Images into GenerateXML

**Files:**
- Modify: `internal/generator/gen_cpp.go` (rework `generateRibbonXmlHeader`)
- Modify: `internal/generator/generator.go:176-181` (success message)
- Test: `internal/generator/gen_cpp_test.go`

- [ ] **Step 1: Write the failing test** (append to `gen_cpp_test.go`; reuse its existing config/tempdir patterns and the `writeTestPNG` idea from Task 2 — duplicate the helper here if there is no shared test util):

```go
func TestGenerateRibbonImagesHeader(t *testing.T) {
	baseDir := t.TempDir()
	writeTestPNG(t, baseDir, "icon.png") // same helper as ribbon_test.go; duplicate locally
	includeDir := filepath.Join(baseDir, "out")
	if err := os.MkdirAll(includeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Project:  config.ProjectConfig{Name: "p"},
		Commands: []config.Command{{Name: "c1", Handler: "c1"}},
		Ribbon: config.RibbonConfig{Tab: "T", Groups: []config.RibbonGroup{{
			Label: "G", Buttons: []config.RibbonButton{{Label: "B", Command: "c1", Image: "icon.png"}},
		}}},
	}
	if err := generateRibbonHeaders(cfg, includeDir, baseDir); err != nil {
		t.Fatal(err)
	}
	imagesH, err := os.ReadFile(filepath.Join(includeDir, "ribbon_images.h"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"kXllRibbonImg0[]",
		`L"xllgen_img_0"`,
		"GetXllRibbonImages",
		"0x89,", // PNG signature first byte must be embedded
		`#include "com/ribbon_image.h"`,
	} {
		if !strings.Contains(string(imagesH), want) {
			t.Errorf("ribbon_images.h missing %q", want)
		}
	}
	xmlH, err := os.ReadFile(filepath.Join(includeDir, "ribbon_xml.h"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(xmlH), `image="xllgen_img_0"`) {
		t.Error("ribbon_xml.h must reference the embedded image name")
	}
}

func TestGenerateRibbonImagesHeaderEmptyWhenNoFileImages(t *testing.T) {
	baseDir := t.TempDir()
	includeDir := filepath.Join(baseDir, "out")
	if err := os.MkdirAll(includeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Project:  config.ProjectConfig{Name: "p"},
		Commands: []config.Command{{Name: "c1", Handler: "c1"}},
		Ribbon: config.RibbonConfig{Tab: "T", Groups: []config.RibbonGroup{{
			Label: "G", Buttons: []config.RibbonButton{{Label: "B", Command: "c1", Image: "HappyFace"}},
		}}},
	}
	if err := generateRibbonHeaders(cfg, includeDir, baseDir); err != nil {
		t.Fatal(err)
	}
	imagesH, err := os.ReadFile(filepath.Join(includeDir, "ribbon_images.h"))
	if err != nil {
		t.Fatal(err) // header must exist even with zero images (unconditional include)
	}
	if !strings.Contains(string(imagesH), "GetXllRibbonImages") {
		t.Error("empty ribbon_images.h must still define GetXllRibbonImages")
	}
}
```

NOTE: adjust `config.ProjectConfig` field/type names to whatever `config.go` actually uses (check `Project` struct) — copy from existing tests in the file.

- [ ] **Step 2: Run, verify failure**

Run: `go test ./internal/generator/ -run TestGenerateRibbonImages -v`
Expected: FAIL — `undefined: generateRibbonHeaders`

- [ ] **Step 3: Implement** in `gen_cpp.go` — rename/rework `generateRibbonXmlHeader` (new import: `strings`):

```go
// generateRibbonHeaders writes ribbon_xml.h (embedded customUI XML) and
// ribbon_images.h (embedded image bytes + lookup table) for ribbon-enabled
// projects. ribbon_images.h is emitted even with zero file images so the
// template's #include is unconditional.
func generateRibbonHeaders(cfg *config.Config, dir string, baseDir string) error {
	if !cfg.Ribbon.Enabled() {
		return nil
	}
	var xmlStr string
	var imgs []ribbon.Image
	var err error
	if cfg.Ribbon.XML != "" {
		// Raw mode: no file images in v1 (loadImage is rejected by ValidateRawXML).
		xmlStr, err = ribbon.ValidateRawXML(filepath.Join(baseDir, cfg.Ribbon.XML), cfg.Commands)
	} else {
		var names map[string]string
		imgs, names, err = ribbon.Images(cfg, baseDir)
		if err == nil {
			xmlStr, err = ribbon.GenerateXML(cfg, names)
		}
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
	if err := os.WriteFile(filepath.Join(dir, "ribbon_xml.h"), []byte(content), 0o644); err != nil {
		return err
	}
	return writeRibbonImagesHeader(imgs, dir)
}

// writeRibbonImagesHeader emits ribbon_images.h: one byte array per
// deduplicated image plus GetXllRibbonImages() returning the name->bytes
// table consumed by xll::ribbon::SetRibbonImages.
func writeRibbonImagesHeader(imgs []ribbon.Image, dir string) error {
	var b strings.Builder
	b.WriteString("// Code generated by xll-gen. DO NOT EDIT.\n#pragma once\n")
	b.WriteString("#include \"com/ribbon_image.h\"\n#include <vector>\n\n")
	for i, img := range imgs {
		fmt.Fprintf(&b, "inline const unsigned char kXllRibbonImg%d[] = {", i)
		for j, by := range img.Data {
			if j%16 == 0 {
				b.WriteString("\n    ")
			}
			fmt.Fprintf(&b, "0x%02X,", by)
		}
		b.WriteString("\n};\n")
	}
	b.WriteString("\ninline std::vector<xll::ribbon::RibbonImage> GetXllRibbonImages() {\n    return {\n")
	for i, img := range imgs {
		fmt.Fprintf(&b, "        { L\"%s\", kXllRibbonImg%d, sizeof(kXllRibbonImg%d) },\n", img.Name, i, i)
	}
	b.WriteString("    };\n}\n")
	return os.WriteFile(filepath.Join(dir, "ribbon_images.h"), []byte(b.String()), 0o644)
}
```

(`fmt` import needed.) Update `generator.go:176-181`:

```go
	if err := generateRibbonHeaders(cfg, includeDir, baseDir); err != nil {
		return err
	}
	if cfg.Ribbon.Enabled() {
		ui.PrintSuccess("Generated", "ribbon_xml.h")
		ui.PrintSuccess("Generated", "ribbon_images.h")
	}
```

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/... -count=1` and `go build ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/generator/gen_cpp.go internal/generator/gen_cpp_test.go internal/generator/generator.go
git commit -m "feat(generator): emit ribbon_images.h with embedded icon bytes"
```

---

### Task 4: C++ assets — GDI+ decoder TU + LoadRibbonImage dispatch

**Files:**
- Create: `internal/assets/files/include/com/ribbon_image.h`
- Create: `internal/assets/files/src/ribbon_image.cpp`
- Modify: `internal/assets/files/src/ribbon_addin.cpp` (GetIDsOfNames + Invoke)

No local C++ test harness exists; this task is compile-verified in Task 6 (regtest builds and **runs** this TU). Keep `ribbon_image.cpp` free of SHM/xll_log/lifecycle includes — that standalone-ness is what makes the regtest possible.

- [ ] **Step 1: Create `include/com/ribbon_image.h`**

```cpp
#pragma once
#include <windows.h>
#include <ocidl.h> // IPictureDisp
#include <vector>

namespace xll { namespace ribbon {

    // One embedded ribbon image. The byte arrays live in the generated
    // ribbon_images.h (process lifetime), so plain pointers are safe.
    struct RibbonImage {
        const wchar_t* name;
        const unsigned char* data;
        size_t size;
    };

    // Set once from xlAutoOpen before the COM add-in connects (same
    // set-before-connect contract as SetRibbonXml); read-only afterwards.
    void SetRibbonImages(std::vector<RibbonImage> images);

    // Decodes PNG/JPG/BMP/GIF/ICO bytes via GDI+ into a 32bpp premultiplied-
    // ARGB IPictureDisp (PNG alpha preserved). Returns nullptr on failure.
    // Lazily starts GDI+ on first use — call only from a normal thread
    // (Excel's STA / a test main), never from DllMain. Caller owns the
    // returned reference.
    IPictureDisp* LoadPictureFromBytes(const unsigned char* data, size_t size);

    // Looks up an embedded image by its customUI name (image="...") and
    // decodes it. nullptr if the name is unknown or decoding fails.
    IPictureDisp* CreateRibbonPicture(const wchar_t* name);

    // GdiplusShutdown iff a decode started it. Generated xlAutoClose calls
    // this after the ribbon add-in disconnects (no further loadImage can
    // arrive; xlAutoClose runs on the same STA thread as Invoke). Already-
    // created pictures stay valid: they own plain GDI HBITMAPs, independent
    // of GDI+ once created.
    void ShutdownRibbonImageEngine();

}} // namespace xll::ribbon
```

- [ ] **Step 2: Create `src/ribbon_image.cpp`**

```cpp
// Standalone GDI+ ribbon image decoder. Deliberately free of SHM / logging /
// lifecycle dependencies so the regtest mock host can compile this TU
// directly. The whole body is guarded: ribbon-less projects glob src/*.cpp
// but must not acquire a gdiplus/shlwapi link requirement.
#ifdef XLL_RIBBON_ENABLED
#include "com/ribbon_image.h"

// MinGW's gdiplus.h needs min/max visible inside namespace Gdiplus (windows.h
// may have NOMINMAX in effect anywhere in the ecosystem).
#include <algorithm>
namespace Gdiplus { using std::min; using std::max; }
#include <gdiplus.h>

#include <shlwapi.h> // SHCreateMemStream
#include <olectl.h>  // OleCreatePictureIndirect, PICTDESC
#include <mutex>

namespace {
    std::vector<xll::ribbon::RibbonImage> g_images;
    std::once_flag g_gdiplusOnce;
    ULONG_PTR g_gdiplusToken = 0;
    bool g_gdiplusStarted = false;

    bool EnsureGdiplus() {
        std::call_once(g_gdiplusOnce, []() {
            Gdiplus::GdiplusStartupInput input;
            g_gdiplusStarted =
                (Gdiplus::GdiplusStartup(&g_gdiplusToken, &input, nullptr) == Gdiplus::Ok);
        });
        return g_gdiplusStarted;
    }

    // Copies the decoded bitmap into a top-down 32bpp premultiplied-ARGB DIB
    // section; the ribbon renders per-pixel alpha only from this layout.
    HBITMAP ToPARGBBitmap(Gdiplus::Bitmap& bmp) {
        const UINT w = bmp.GetWidth(), h = bmp.GetHeight();
        if (w == 0 || h == 0) return nullptr;

        BITMAPINFO bi = {};
        bi.bmiHeader.biSize = sizeof(bi.bmiHeader);
        bi.bmiHeader.biWidth = static_cast<LONG>(w);
        bi.bmiHeader.biHeight = -static_cast<LONG>(h); // top-down, matches LockBits
        bi.bmiHeader.biPlanes = 1;
        bi.bmiHeader.biBitCount = 32;
        bi.bmiHeader.biCompression = BI_RGB;

        void* bits = nullptr;
        HBITMAP hbmp = CreateDIBSection(nullptr, &bi, DIB_RGB_COLORS, &bits, nullptr, 0);
        if (!hbmp || !bits) {
            if (hbmp) DeleteObject(hbmp);
            return nullptr;
        }

        Gdiplus::Rect rect(0, 0, static_cast<INT>(w), static_cast<INT>(h));
        Gdiplus::BitmapData data = {};
        data.Width = w;
        data.Height = h;
        data.Stride = static_cast<INT>(w * 4);
        data.PixelFormat = PixelFormat32bppPARGB;
        data.Scan0 = bits;
        if (bmp.LockBits(&rect,
                         Gdiplus::ImageLockModeRead | Gdiplus::ImageLockModeUserInputBuf,
                         PixelFormat32bppPARGB, &data) != Gdiplus::Ok) {
            DeleteObject(hbmp);
            return nullptr;
        }
        bmp.UnlockBits(&data);
        return hbmp;
    }
}

namespace xll { namespace ribbon {

    void SetRibbonImages(std::vector<RibbonImage> images) { g_images = std::move(images); }

    IPictureDisp* LoadPictureFromBytes(const unsigned char* data, size_t size) {
        if (!data || size == 0 || !EnsureGdiplus()) return nullptr;

        IStream* stream = SHCreateMemStream(data, static_cast<UINT>(size));
        if (!stream) return nullptr;

        IPictureDisp* picture = nullptr;
        {
            // Scoped: the Gdiplus::Bitmap must be destroyed while GDI+ is up.
            Gdiplus::Bitmap bmp(stream);
            if (bmp.GetLastStatus() == Gdiplus::Ok) {
                if (HBITMAP hbmp = ToPARGBBitmap(bmp)) {
                    PICTDESC pd = {};
                    pd.cbSizeofstruct = sizeof(pd);
                    pd.picType = PICTYPE_BITMAP;
                    pd.bmp.hbitmap = hbmp;
                    if (FAILED(OleCreatePictureIndirect(
                            &pd, IID_IPictureDisp, TRUE,
                            reinterpret_cast<void**>(&picture)))) {
                        DeleteObject(hbmp); // fOwn=TRUE transfers only on success
                        picture = nullptr;
                    }
                }
            }
        }
        stream->Release();
        return picture;
    }

    IPictureDisp* CreateRibbonPicture(const wchar_t* name) {
        if (!name) return nullptr;
        for (const auto& img : g_images) {
            if (_wcsicmp(img.name, name) == 0) {
                return LoadPictureFromBytes(img.data, img.size);
            }
        }
        return nullptr;
    }

    void ShutdownRibbonImageEngine() {
        if (g_gdiplusStarted) {
            Gdiplus::GdiplusShutdown(g_gdiplusToken);
            g_gdiplusStarted = false;
            g_gdiplusToken = 0;
        }
    }

}} // namespace xll::ribbon
#endif // XLL_RIBBON_ENABLED
```

- [ ] **Step 3: Wire the `LoadRibbonImage` callback into `src/ribbon_addin.cpp`**

Inside the `#ifdef XLL_RIBBON_ENABLED` region (after line 88), add the include and dispid:

```cpp
#include "com/ribbon_image.h"
```

In the anonymous namespace next to `kDispIdExtBase` (~line 95):

```cpp
    // loadImage="LoadRibbonImage" callback. Commands start at kDispIdBase
    // (1000), extensibility ids are negative — 999 cannot collide.
    constexpr DISPID kDispIdLoadImage = 999;
```

In `GetIDsOfNames` (line 135), after the `kExtNames` loop, before the command-name loop:

```cpp
    if (_wcsicmp(rgszNames[0], L"LoadRibbonImage") == 0) {
        rgDispId[0] = kDispIdLoadImage;
        return S_OK;
    }
```

In `Invoke` (line 153): name the 6th parameter `pVarResult` (currently anonymous `VARIANT*`), and insert after the extensibility early-return (line 156):

```cpp
    if (dispIdMember == kDispIdLoadImage) {
        // loadImage(imageId As String) As IPictureDisp — imageId arrives as
        // VT_BSTR (args reversed); the picture goes back via pVarResult and
        // Office takes ownership of the reference.
        if (!pDispParams || pDispParams->cArgs < 1 || !pVarResult) return E_INVALIDARG;
        VARIANT& v = pDispParams->rgvarg[pDispParams->cArgs - 1];
        BSTR name = nullptr;
        if (v.vt == VT_BSTR) {
            name = v.bstrVal;
        } else if (v.vt == (VT_BSTR | VT_BYREF) && v.pbstrVal) {
            name = *v.pbstrVal;
        }
        if (!name) return E_INVALIDARG;
        IPictureDisp* pic = xll::ribbon::CreateRibbonPicture(name);
        if (!pic) {
            // Office shows a blank icon on E_FAIL; never popup, never crash.
            xll::LogWarn("Ribbon: loadImage failed for " +
                         WideToUtf8(std::wstring(name, SysStringLen(name))));
            return E_FAIL;
        }
        VariantInit(pVarResult);
        pVarResult->vt = VT_DISPATCH;
        pVarResult->pdispVal = pic;
        return S_OK;
    }
```

VERIFY: the exact warn-level log function name in `include/xll_log.h` (`xll::LogWarn` assumed — `ribbon_addin.cpp` already uses `xll::LogDebug`; adjust if the header spells it differently). `WideToUtf8` is already used at line 174.

- [ ] **Step 4: Verify Go side still builds** (C++ compiles in Task 6)

Run: `go test ./internal/... -count=1`
Expected: PASS (assets are go:embed'd; no Go code change here)

- [ ] **Step 5: Commit**

```bash
git add internal/assets/files/include/com/ribbon_image.h internal/assets/files/src/ribbon_image.cpp internal/assets/files/src/ribbon_addin.cpp
git commit -m "feat(assets): GDI+ ribbon image decoder + LoadRibbonImage callback"
```

---

### Task 5: templates — wire SetRibbonImages, GDI+ shutdown, and link libs

**Files:**
- Modify: `internal/templates/xll_main.cpp.tmpl` (~lines 54-56, ~line 533, lines 166-182)
- Modify: `internal/templates/CMakeLists.txt.tmpl` (lines 171-183)
- Test: `internal/generator/gen_cpp_test.go`

- [ ] **Step 1: Write the failing test** (append to `gen_cpp_test.go`, following its existing generateCppMain/generateCMake test patterns — find how existing tests render templates and assert on output):

```go
func TestXllMainRibbonImageWiring(t *testing.T) {
	// Render xll_main.cpp + CMakeLists.txt for a ribbon-enabled config (reuse
	// the rendering helper pattern of the existing ribbon template tests in
	// this file) and assert the new wiring:
	out := renderXllMainForRibbonConfig(t) // adapt to the file's existing helper; inline if none
	for _, want := range []string{
		`#include "ribbon_images.h"`,
		"xll::ribbon::SetRibbonImages(GetXllRibbonImages());",
		"xll::ribbon::ShutdownRibbonImageEngine();",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("xll_main.cpp missing %q", want)
		}
	}
	cmake := renderCMakeForRibbonConfig(t) // same adaptation note
	for _, want := range []string{"gdiplus", "shlwapi"} {
		if !strings.Contains(cmake, want) {
			t.Errorf("CMakeLists.txt missing link lib %q", want)
		}
	}
}
```

NOTE: this file already has template-rendering tests for the ribbon feature (it was in the v0.4.0 task list) — copy their exact setup instead of inventing helpers.

- [ ] **Step 2: Run, verify failure**

Run: `go test ./internal/generator/ -run TestXllMainRibbonImageWiring -v`
Expected: FAIL (missing strings)

- [ ] **Step 3: Implement template edits**

`xll_main.cpp.tmpl` — include block (~line 54):

```
{{if .Ribbon.Enabled}}
#include "ribbon_xml.h"
#include "ribbon_images.h"
{{end}}
```

Bootstrap (~line 533), directly after `xll::ribbon::SetRibbonXml(kXllRibbonXml);`:

```
        xll::ribbon::SetRibbonImages(GetXllRibbonImages());
```

`xlAutoClose` ribbon teardown block — after `rtd::UnregisterServer(GetRibbonClsid(), g_szRibbonProgID);` (line 180), still inside the same `XLL_SAFE_BLOCK`:

```
        // (1b) GDI+ down only after disconnect: loadImage callbacks arrive on
        // this same STA thread, so none can be in flight here. Created
        // pictures survive (plain GDI bitmaps).
        xll::ribbon::ShutdownRibbonImageEngine();
```

`CMakeLists.txt.tmpl` ribbon block (lines 176-181) — extend the lib list:

```
    target_link_libraries(${PROJECT_NAME} PRIVATE
        ole32
        oleaut32
        uuid
        oleacc
        gdiplus
        shlwapi
    )
```

(gdiplus/shlwapi: ribbon_image.cpp decoder. Only the ribbon-enabled block needs them — the decoder TU compiles empty without `XLL_RIBBON_ENABLED`.)

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/generator/ -count=1 -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/templates/xll_main.cpp.tmpl internal/templates/CMakeLists.txt.tmpl internal/generator/gen_cpp_test.go
git commit -m "feat(templates): wire ribbon image table, GDI+ shutdown, and link libs"
```

---

### Task 6: regtest — compile & run the decode path end-to-end

**Files:**
- Create: `internal/regtest/testdata/icon.png` (16x16 PNG with alpha)
- Modify: `internal/regtest/testdata/xll.yaml` (add ribbon section)
- Modify: `internal/regtest/assets.go` (embed icon.png)
- Modify: `internal/regtest/testdata/CMakeLists.txt` (compile ribbon_image.cpp, link libs)
- Modify: `internal/regtest/testdata/mock_host.cpp` (Test 15)
- Modify: `cmd/regression_test.go` (write icon.png into temp project)

- [ ] **Step 1: Create the PNG fixture**

Write this throwaway program as `gen_icon.go` in the repo root, run it, delete it:

```go
//go:build ignore

package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
)

func main() {
	img := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.Set(x, y, color.NRGBA{R: 220, G: 60, B: 40, A: uint8(255 - y*12)})
		}
	}
	f, err := os.Create("internal/regtest/testdata/icon.png")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
}
```

Run: `go run gen_icon.go` then `Remove-Item gen_icon.go`
Expected: `internal/regtest/testdata/icon.png` exists (a few hundred bytes).

- [ ] **Step 2: Wire fixtures**

`internal/regtest/testdata/xll.yaml` — add after the `commands:` block:

```yaml
# Ribbon with a file image. Exercises ribbon_images.h generation; the mock
# host compiles the asset decoder TU and round-trips icon.png -> IPictureDisp.
ribbon:
  tab: "Smoke"
  groups:
    - label: "Test"
      buttons:
        - label: "Run"
          command: "RunReport"
          image: "./icon.png"
```

`internal/regtest/assets.go` — append:

```go
//go:embed testdata/icon.png
var IconPng []byte
```

`cmd/regression_test.go` — directly after the xll.yaml write (line 69-71):

```go
	if err := os.WriteFile(filepath.Join(projectDir, "icon.png"), regtest.IconPng, 0644); err != nil {
		t.Fatal(err)
	}
```

`internal/regtest/testdata/CMakeLists.txt` — replace lines 50-55 with:

```cmake
# ribbon_image.cpp is the standalone GDI+ decoder asset; compiling it here
# regression-tests the embed->decode->IPictureDisp chain the real XLL uses
# for loadImage callbacks (see mock_host test 15).
add_executable(mock_host main.cpp ../generated/cpp/src/ribbon_image.cpp)
target_compile_definitions(mock_host PRIVATE XLL_RIBBON_ENABLED)
target_include_directories(mock_host PRIVATE
    ../generated/cpp
    ../generated/cpp/include
)
target_link_libraries(mock_host PRIVATE shm xll-gen-types flatbuffers
    gdiplus shlwapi ole32 oleaut32 uuid)
```

- [ ] **Step 3: Add mock_host Test 15**

In `internal/regtest/testdata/mock_host.cpp`, add includes near the top with the other includes:

```cpp
#include "com/ribbon_image.h"
#include "ribbon_images.h"
```

Insert before `cout << "PASSED" << endl;` (line 611):

```cpp
    // 15. Ribbon image decode (generated ribbon_images.h -> GDI+ -> IPictureDisp).
    // Purely local (no server round-trip): verifies the exact embed+decode
    // chain the XLL's loadImage callback uses, including alpha-capable PNG.
    {
        if (FAILED(CoInitialize(nullptr))) {
            cerr << "FAIL: CoInitialize for ribbon image test" << endl;
            return 1;
        }
        xll::ribbon::SetRibbonImages(GetXllRibbonImages());

        IPictureDisp* pic = xll::ribbon::CreateRibbonPicture(L"xllgen_img_0");
        if (!pic) {
            cerr << "FAIL: CreateRibbonPicture(xllgen_img_0) returned null" << endl;
            return 1;
        }
        IPicture* p = nullptr;
        if (FAILED(pic->QueryInterface(IID_IPicture, (void**)&p)) || !p) {
            cerr << "FAIL: IPictureDisp -> IPicture QI" << endl;
            return 1;
        }
        OLE_XSIZE_HIMETRIC w = 0;
        OLE_YSIZE_HIMETRIC h = 0;
        p->get_Width(&w);
        p->get_Height(&h);
        if (w <= 0 || h <= 0) {
            cerr << "FAIL: decoded picture has empty extent" << endl;
            return 1;
        }
        // Unknown names must fail cleanly (blank icon path), never crash.
        if (xll::ribbon::CreateRibbonPicture(L"no_such_image") != nullptr) {
            cerr << "FAIL: unknown image name must return null" << endl;
            return 1;
        }
        p->Release();
        pic->Release();
        xll::ribbon::ShutdownRibbonImageEngine();
        CoUninitialize();
    }
```

- [ ] **Step 4: Run the full regression test** (Windows, needs cmake; ~minutes on a cold FetchContent cache)

Run, in `xll-gen/`: `go test ./cmd -run TestRegression -count=1 -v -timeout 30m`
Expected: PASS with `[MOCK] PASSED` in output. If the mock host build fails, fix the C++ from Task 4 (this is the task where it first compiles). Process-cleanup note: the harness force-kills mock host and server in defers — do not weaken that.

- [ ] **Step 5: Run everything else**

Run: `go test ./... -count=1 -short` then `go vet ./...`
Expected: PASS / clean

- [ ] **Step 6: Commit**

```bash
git add internal/regtest/testdata internal/regtest/assets.go cmd/regression_test.go
git commit -m "test(regtest): compile and run the ribbon image decode path (test 15)"
```

---

### Task 7: docs + backlog

**Files:**
- Modify: `README.md` (ribbon section — find it with `grep -n "imageMso\|ribbon" README.md`)
- Modify: `docs/ribbon-e2e.md` (manual checklist)
- Modify: `../IMPROVEMENT_BACKLOG.md` (workspace root, NOT inside xll-gen)

- [ ] **Step 1: README** — in the ribbon section where `image:` is documented, replace/extend the imageMso text:

```markdown
`image:` accepts either a built-in Office icon name (`imageMso`, e.g.
`HappyFace`) or a path to an image file relative to `xll.yaml`
(`.png .jpg .jpeg .bmp .gif .ico`). File images are embedded into the XLL at
build time and decoded with GDI+ at runtime — PNG transparency is preserved.
Recommended sizes: 16×16 (size: normal), 32×32 (size: large). JPG has no
alpha channel, so JPG icons render as opaque squares.

```yaml
buttons:
  - label: "Refresh"
    command: refresh_all
    image: ./icons/refresh.png   # file -> embedded
  - label: "Smile"
    command: smile
    image: HappyFace             # imageMso -> built-in
```
```

Adapt the snippet to the README's actual ribbon example so it stays consistent.

- [ ] **Step 2: ribbon-e2e.md** — append a checklist item:

```markdown
- [ ] **PNG file icon**: declare a button with `image: ./icons/<file>.png`
  (use a PNG with transparent corners). Build, load in Excel: the icon renders
  on the ribbon with transparency intact at both `size: normal` and
  `size: large`. Delete the PNG, rebuild: generation fails with a clear
  file-not-found error naming the button.
```

- [ ] **Step 3: Backlog** — append to `../IMPROVEMENT_BACKLOG.md` under xll-gen's section, following the file's existing item format:

```markdown
- ☐ ribbon: raw-XML mode image support — `ribbon.images: {name: path}` map
  paired with allowing `loadImage="LoadRibbonImage"` in user-authored customUI
  (v1 deliberately rejects it; structured mode only).
- ☐ showcase: add a PNG file-icon ribbon button to the showcase XLL
  (exercises ribbon_images.h in a real Excel E2E).
```

- [ ] **Step 4: Commit**

```bash
git add README.md docs/ribbon-e2e.md
git commit -m "docs: ribbon image file usage, e2e checklist item"
```

(Backlog file is outside the repo — no git add for it.)

---

## Final verification (before review/merge)

1. `go test ./... -count=1` — all green (regression test included; allow long timeout).
2. `go vet ./...` — clean.
3. Spec cross-check: every spec section maps to a task (YAML detection→T1, loader/XML→T2, header→T3, decoder/dispatch→T4, wiring/links→T5, tests→T1-6, docs→T7; raw-XML mode intentionally unchanged).
4. Reviewers to run (per repo practice): `xll-cpp-reviewer` on the C++ asset/template changes, `memory-safety-auditor` on GDI+/IPictureDisp/HBITMAP ownership.
