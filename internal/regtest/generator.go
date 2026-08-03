package regtest

import (
	"os"
	"path/filepath"
	"text/template"

	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/internal/templates"
	"github.com/xll-gen/xll-gen/pkg/msgid"
)

// generateSimMain generates the C++ main file for the simulation host.
func generateSimMain(cfg *config.Config, dir string) error {
	tmplContent, err := templates.Get("regtest_main.cpp.tmpl")
	if err != nil {
		return err
	}

	funcMap := template.FuncMap{
		"add": func(a, b int) int { return a + b },
		// MsgUserStart mirrors internal/generator/funcmap.go's helper of the
		// same name so the simulation host computes user-function message IDs
		// from the SAME base as the generated XLL and the generated Go dispatch
		// (pkg/msgid is the Go-side SSOT, AGENTS.md §18.6). Without it the
		// template had to write a number, and the number it wrote (11) was both
		// wrong and transport-reserved.
		"MsgUserStart": func() int { return msgid.MsgUserStart },
		// isRtdLike gates the per-function probe block. It MUST answer the same
		// way internal/generator's funcmap does, because the probe is only
		// meaningful for a function the generated Go dispatch has a
		// `case MsgUserStart+i` for — hence the shared config.IsRtdLike SSOT
		// rather than a second copy of the predicate here.
		"isRtdLike": config.IsRtdLike,
		// anyNonRtdLike answers "does this project have ANY function this host
		// can probe at all?". Gating only the per-function block on isRtdLike
		// left an all-rtd project rendering a main() with ZERO probes:
		// `failures` stayed 0, main returned 0, and runner.go reports success
		// on the exit code alone — the same "green with no signal" the
		// per-function skip was meant to remove, just moved up one level.
		// Shared with internal/generator through config.AnyNonRtdLike.
		"anyNonRtdLike": config.AnyNonRtdLike,
	}

	t, err := template.New("regtest_main").Funcs(funcMap).Parse(tmplContent)
	if err != nil {
		return err
	}

	f, err := os.Create(filepath.Join(dir, "main.cpp"))
	if err != nil {
		return err
	}
	defer f.Close()

	return t.Execute(f, cfg)
}

// generateSimCMake writes the CMakeLists.txt for the simulation host.
//
// Copied VERBATIM, not rendered: regtest_CMakeLists.txt.tmpl contains no
// template actions at all. Running a static file through text/template bought
// nothing and carried a trap — CMake generator expressions and any future
// `{{` in that file would have been parsed as actions, failing or, worse,
// rendering to empty. It keeps the .tmpl extension so the embed pattern and the
// "everything the generator emits lives in internal/templates" convention hold.
func generateSimCMake(cfg *config.Config, dir string) error {
	_ = cfg // static content; kept in the signature for symmetry with its siblings
	content, err := templates.Get("regtest_CMakeLists.txt.tmpl")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "CMakeLists.txt"), []byte(content), 0o644)
}
