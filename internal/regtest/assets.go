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
