package assets

import (
	"embed"
	"io/fs"
	"path/filepath"
	"strings"
)

// assetsFS embeds all files in the files/ directory.
//
//go:embed files/*
var assetsFS embed.FS

// AssetsMap holds the content of all embedded assets, keyed by relative path (e.g., "xlcall.h" or "tools/compressor.cpp").
// It is populated during package initialization.
var AssetsMap = make(map[string]string)

// init walks the embedded filesystem and populates AssetsMap.
func init() {
	// Populate AssetsMap from embedded files
	// The root is "files"
	err := fs.WalkDir(assetsFS, "files", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			content, err := assetsFS.ReadFile(path)
			if err != nil {
				return err
			}

			// Compute relative path from "files" root
			// path is "files/tools/compressor.cpp", we want "tools/compressor.cpp"
			// path is "files/xlcall.h", we want "xlcall.h"
			relPath := strings.TrimPrefix(path, "files/")

			// Use the relative path as the key to preserve directory structure
			// Ensure we use forward slashes for consistency across platforms if needed,
			// though embedded FS usually uses forward slashes.
			relPath = filepath.ToSlash(relPath)

			AssetsMap[relPath] = string(content)
		}
		return nil
	})
	if err != nil {
		panic(err)
	}
}
