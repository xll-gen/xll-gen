// Package ribbon generates and validates Office customUI ribbon XML for
// xll-gen projects. Structured mode (tab/groups in xll.yaml) generates the
// XML; raw mode validates a user-authored customUI file against the
// declared commands.
package ribbon

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
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
// the generated ribbon_xml.h. The delimiter is fixed; XML containing the
// closing sequence is rejected (it cannot occur in well-formed customUI XML).
func ToCppRawLiteral(xmlStr string) (string, error) {
	const delim = "XLLRIBBON"
	if strings.Contains(xmlStr, ")"+delim+`"`) {
		return "", fmt.Errorf("ribbon xml contains the raw-literal delimiter sequence )%s\"", delim)
	}
	return `LR"` + delim + `(` + xmlStr + `)` + delim + `"`, nil
}
