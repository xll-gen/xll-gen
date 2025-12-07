package regtest

import (
	_ "embed"
)

//go:embed assets/mock_host.cpp
var MockHostCpp string

//go:embed assets/xll.yaml
var XllYaml string

//go:embed assets/server.go
var ServerGo string

//go:embed assets/CMakeLists.txt
var CMakeLists string
