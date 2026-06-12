package regtest

import (
	_ "embed"
)

//go:embed testdata/mock_host.cpp
var MockHostCpp string

//go:embed testdata/xll.yaml
var XllYaml string

//go:embed testdata/server.go
var ServerGo string

//go:embed testdata/CMakeLists.txt
var CMakeLists string

// IconPng is a 16x16 RGBA PNG (red with a vertical alpha gradient) used as
// the ribbon button image fixture. Regenerate with Go stdlib if ever needed:
// image.NewNRGBA(16x16), color.NRGBA{220,60,40, 255-y*12}, png.Encode.
//
//go:embed testdata/icon.png
var IconPng []byte
