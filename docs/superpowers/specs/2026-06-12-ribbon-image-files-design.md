# Ribbon Image Files (PNG/JPG) — Design

**Date:** 2026-06-12
**Status:** Approved for implementation (follows 2026-06-11 ribbon-command-design)
**Repo scope:** xll-gen only

## Goal

Let `ribbon.groups[].buttons[].image` in `xll.yaml` accept an image **file path**
(PNG/JPG/JPEG/BMP/GIF/ICO) in addition to the existing built-in `imageMso`
names. No manual conversion by the user: xll-gen embeds the file bytes into the
XLL at build time and serves them to Office at runtime.

## Background / why it can't be XML-only

- The ribbon is hosted by a COM add-in (`RibbonAddIn`). For COM add-ins, custom
  control images require the `image="name"` attribute **plus** a `loadImage`
  callback on `<customUI>`; Office calls it expecting an `IPictureDisp*`.
- `OleLoadPicture` cannot decode PNG. GDI+ decodes PNG/JPG/BMP/GIF/ICO and
  preserves PNG alpha. The proven path (Excel-DNA, NetOffice):
  bytes → `IStream` → `Gdiplus::Bitmap` → 32bpp pre-multiplied-ARGB `HBITMAP`
  → `OleCreatePictureIndirect` → `IPictureDisp`.

## YAML surface (no new fields)

The existing `image:` field is overloaded with deterministic detection:

1. Value contains a path separator (`/` or `\`) **or** ends with a known image
   extension (`.png .jpg .jpeg .bmp .gif .ico`, case-insensitive) → **file
   path**, resolved relative to the directory containing `xll.yaml`.
2. Value contains a path separator but has an unknown/missing extension →
   **build error** ("unsupported ribbon image format").
3. Otherwise → `imageMso` name (unchanged behavior; mso names never contain
   `.` or path separators).

```yaml
ribbon:
  tab: "My Tools"
  groups:
    - label: "Data"
      buttons:
        - label: "Refresh"
          command: refresh_all
          image: ./icons/refresh.png   # file → embedded + loadImage
        - label: "Settings"
          command: open_settings
          image: HappyFace             # imageMso → unchanged
```

## Build-time pipeline (Go)

### config (`internal/config`)
- `RibbonButton.ImageIsFile() bool` + shared extension table. Pure shape
  checks only; file-IO validation stays out of `config.Validate` (consistent
  with raw-XML handling, which is validated in the generator with `baseDir`).

### ribbon (`internal/ribbon`)
- `Images(cfg *config.Config, baseDir string) ([]Image, error)` where
  `Image{Name, Path string; Data []byte}`:
  - Resolves each file-image button, reads bytes, **dedupes by cleaned path**
    (one blob per file, shared by multiple buttons).
  - Validation: file exists & readable, non-empty, ≤ 1 MiB per file (icons;
    prevents XLL bloat — error tells the user to shrink), extension in the
    supported set. Errors carry the offending path and button label.
  - Names are deterministic: `xllgen_img_<dedupIndex>`.
- `GenerateXML`:
  - File-image buttons emit `image="xllgen_img_<i>"` instead of `imageMso`.
  - When ≥1 file image exists, `<customUI ... loadImage="LoadRibbonImage">`.
- Raw-XML mode is **unchanged in v1**: `loadImage` in user XML is still
  rejected. Follow-up backlog item: `ribbon.images: {name: path}` map to pair
  with raw XML.

### generator (`internal/generator`)
- New `ribbon_images.h` emitted next to `ribbon_xml.h` when file images exist:
  byte arrays (`inline const unsigned char kXllRibbonImg0[] = {...};`) plus a
  name→(ptr,size) table. Always emit the header (empty table) when the ribbon
  is enabled, so the template include is unconditional.
- `xll_main.cpp.tmpl`: call `xll::ribbon::SetRibbonImages(...)` alongside
  `SetRibbonXml` (set-before-connect contract).

## Runtime (C++ assets)

### `com/ribbon_addin.h` / `src/ribbon_addin.cpp`
- `struct RibbonImage { std::wstring name; const unsigned char* data; size_t size; };`
- `void SetRibbonImages(std::vector<RibbonImage>)` — set once from xlAutoOpen,
  read-only afterwards (same contract as `SetRibbonXml`).
- `GetIDsOfNames`: recognize `LoadRibbonImage` → `kDispIdLoadImage = 999`
  (just below `kDispIdBase = 1000`; extensibility ids are negative — no
  collision).
- `Invoke(kDispIdLoadImage)`: arg is the image name as `VT_BSTR` (handle
  `VT_BSTR|VT_BYREF` too, args reversed as with onAction). Lookup → decode:
  - Lazy GDI+ init via `std::once_flag` (`GdiplusStartup` on the calling STA
    thread; **never** in DllMain).
  - `SHCreateMemStream` over the embedded bytes → `Gdiplus::Bitmap` →
    32bpp **pre-multiplied** ARGB DIB (`LockBits(PixelFormat32bppPARGB)` +
    `CreateDIBSection` copy, preserving PNG alpha) →
    `OleCreatePictureIndirect(PICTDESC{PICTYPE_BITMAP}, fOwn=TRUE)`.
  - Success: `pVarResult` = `VT_DISPATCH` holding the `IPictureDisp*`
    (Office takes ownership). Missing name / decode failure: log warn,
    return `E_FAIL` (Office shows a blank icon; no popup, no crash).
- `void ShutdownRibbonImages()` — `GdiplusShutdown` iff started; called from
  generated `xlAutoClose` **after** ribbon disconnect + command drain (created
  pictures are plain GDI bitmaps, independent of GDI+ once created).

### Build
- `CMakeLists.txt.tmpl`: link `gdiplus` and `shlwapi` (SHCreateMemStream) to
  the XLL target (always; both are tiny universal system libs).

## Error handling summary

| Failure | When | Behavior |
|---|---|---|
| File missing/unreadable/empty/too big/bad ext | build | generation fails with path + button label |
| Unknown name at loadImage | runtime | warn log, `E_FAIL`, blank icon |
| GDI+ init or decode failure | runtime | warn log, `E_FAIL`, blank icon |

JPG has no alpha channel → opaque square icon (documented). Docs recommend
16×16 (normal) / 32×32 (large) PNG; Office scales other sizes.

## Testing

- **Go unit tests**: detection rules (file vs mso vs error), `Images()`
  validation/dedup, `GenerateXML` emission (`image=` + `loadImage=` attr),
  generator header emission (golden-ish string checks + byte-table contents).
- **Regtest** (`internal/regtest`): extend the ribbon regtest — mock host
  calls `GetIDsOfNames(L"LoadRibbonImage")` then `Invoke` with a name from an
  embedded tiny PNG fixture; asserts `S_OK`, non-null `IPictureDisp`, and sane
  `IPicture::get_Width/get_Height`. This compiles the real decode path
  (lesson from v0.4.1: tests must compile/run generated C++, not just
  string-check it).
- **Manual E2E**: new checklist item in `docs/ribbon-e2e.md` (PNG-with-alpha
  button renders on a real Excel ribbon).

## Out of scope (backlog)

- Raw-XML mode image map (`ribbon.images`).
- `getImage` dynamic callback, DPI-variant image sets.
- showcase repo update (separate repo; follow-up).
