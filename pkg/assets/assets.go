package assets

import (
	"embed"
	"io/fs"
)

//go:embed files/*
var assetsFS embed.FS

var AssetsMap = make(map[string]string)

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
