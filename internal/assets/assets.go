package assets

import (
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
)

// assetsFS embeds every file under files/ at compile time.
//
//go:embed files/*
var assetsFS embed.FS

var (
	once     sync.Once
	assetMap map[string]string
	loadErr  error
)

// Assets returns the embedded asset map keyed by forward-slash relative path
// (e.g. "xlcall.h", "tools/compressor.cpp"). The walk runs once on first
// call; any error is cached and returned on subsequent calls.
//
// Prior versions populated a package var in init() and panicked on walk
// failure, taking down every importer of this package — including read-only
// tools that never need the assets. Lazy load + returned error lets callers
// surface the failure at the point of first use.
func Assets() (map[string]string, error) {
	once.Do(func() {
		m := make(map[string]string)
		walkErr := fs.WalkDir(assetsFS, "files", func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			content, rerr := assetsFS.ReadFile(path)
			if rerr != nil {
				return fmt.Errorf("read %s: %w", path, rerr)
			}
			rel := filepath.ToSlash(strings.TrimPrefix(path, "files/"))
			m[rel] = string(content)
			return nil
		})
		if walkErr != nil {
			loadErr = fmt.Errorf("embed asset walk: %w", walkErr)
			return
		}
		assetMap = m
	})
	return assetMap, loadErr
}
