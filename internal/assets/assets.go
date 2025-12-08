package assets

import (
	"embed"
	"io/fs"
)

// assetsFS embeds all files in the files/ directory.
//
//go:embed files/*
var assetsFS embed.FS

// AssetsMap holds the content of all embedded assets, keyed by filename.
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
			// Use the filename as the key (e.g., "xlcall.h")
			AssetsMap[d.Name()] = string(content)
		}
		return nil
	})
	if err != nil {
		panic(err)
	}
}
