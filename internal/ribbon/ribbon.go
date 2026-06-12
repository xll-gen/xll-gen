// Package ribbon generates and validates Office customUI ribbon XML for
// xll-gen projects. Structured mode (tab/groups in xll.yaml) generates the
// XML; raw mode validates a user-authored customUI file against the
// declared commands.
package ribbon

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

// GenerateXML renders structured-mode ribbon config into customUI XML.
// Control ids are deterministic (xllgen_btn_<group>_<button>) and flow back
// to Go handlers as CommandContext.ControlID.
//
// imageNames maps each raw yaml image value to the embedded image name
// produced by Images (e.g. "icons/a.png" -> "xllgen_img_0"). A button whose
// Image value is present in the map emits image="<name>"; any other non-empty
// Image value is treated as an imageMso id. When imageNames is nil or empty
// there are no file images, so the loadImage="LoadRibbonImage" attribute is
// omitted from the customUI root element.
func GenerateXML(cfg *config.Config, imageNames map[string]string) (string, error) {
	r := cfg.Ribbon
	if r.XML != "" {
		return "", fmt.Errorf("GenerateXML called in raw-xml mode")
	}
	if r.Tab == "" {
		return "", fmt.Errorf("ribbon.tab is empty")
	}

	var b strings.Builder
	loadImageAttr := ""
	if len(imageNames) > 0 {
		loadImageAttr = ` loadImage="LoadRibbonImage"`
	}
	fmt.Fprintf(&b, `<customUI xmlns="%s"%s><ribbon><tabs><tab id="xllgen_tab" label="%s">`,
		CustomUINamespace, loadImageAttr, escape(r.Tab))
	for gi, g := range r.Groups {
		fmt.Fprintf(&b, `<group id="xllgen_grp_%d" label="%s">`, gi, escape(g.Label))
		for bi, btn := range g.Buttons {
			// Default size locally so GenerateXML is correct even when called
			// without config.ApplyDefaults having normalized the button first.
			size := btn.Size
			if size == "" {
				size = "normal"
			}
			fmt.Fprintf(&b, `<button id="xllgen_btn_%d_%d" label="%s" size="%s" onAction="%s"`,
				gi, bi, escape(btn.Label), escape(size), escape(btn.Command))
			if btn.Image != "" {
				if name, ok := imageNames[btn.Image]; ok {
					fmt.Fprintf(&b, ` image="%s"`, escape(name))
				} else {
					fmt.Fprintf(&b, ` imageMso="%s"`, escape(btn.Image))
				}
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

	dec := xml.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
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
// the generated ribbon_xml.h. The output is pure ASCII: a leading UTF-8 BOM is
// stripped, and every rune above 0x7F is carried as an XML numeric character
// reference (&#x...;) that Office's XML parser expands — this keeps the wide
// literal immune to the compiler's source/execution charset (MSVC would
// otherwise garble e.g. Korean labels). customUI element/attribute names are
// ASCII by schema, so the blanket rune transform is safe. XML containing the
// closing delimiter sequence is rejected (it cannot occur in well-formed
// customUI XML).
func ToCppRawLiteral(xmlStr string) (string, error) {
	const delim = "XLLRIBBON"
	xmlStr = strings.TrimPrefix(xmlStr, "\uFEFF")
	if strings.Contains(xmlStr, ")"+delim+`"`) {
		return "", fmt.Errorf("ribbon xml contains the raw-literal delimiter sequence )%s\"", delim)
	}
	var b strings.Builder
	for _, r := range xmlStr {
		if r > 0x7F {
			fmt.Fprintf(&b, "&#x%X;", r)
		} else {
			b.WriteRune(r)
		}
	}
	return `LR"` + delim + `(` + b.String() + `)` + delim + `"`, nil
}
