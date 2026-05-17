package smoketest

import _ "embed"

//go:embed testdata/xll.yaml
var xllYaml string

//go:embed testdata/main.go
var serverMain string
