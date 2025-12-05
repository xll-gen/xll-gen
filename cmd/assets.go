package cmd

import (
	"embed"
	"io/fs"
)

//go:embed assets/*
var assetsFS embed.FS

var assetsMap = make(map[string]string)

func init() {
	// Populate assetsMap from embedded files
	err := fs.WalkDir(assetsFS, "assets", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			content, err := assetsFS.ReadFile(path)
			if err != nil {
				return err
			}
			// Use the filename as the key (e.g., "xlcall.h")
			// This matches the logic in init.go which expects flat list of filenames to write.
			assetsMap[d.Name()] = string(content)
		}
		return nil
	})
	if err != nil {
		panic(err) // Should not happen with embedded files
	}
}
