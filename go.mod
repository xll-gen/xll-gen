module github.com/xll-gen/xll-gen

go 1.24.3

require (
	github.com/go-ole/go-ole v1.3.0
	github.com/google/flatbuffers v25.9.23+incompatible
	github.com/google/uuid v1.6.0
	github.com/spf13/cobra v1.10.2
	github.com/xll-gen/shm v0.7.5
	github.com/xll-gen/sugar v0.7.1
	github.com/xll-gen/types v0.2.10
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	golang.org/x/sys v0.33.0 // indirect
)

// DEV REPLACE (rtd-once grid spill feature, in-flight): the published types
// v0.2.10 predates the protocol.RtdOnceGridResult table (added in the local
// types checkout, commit 50a9613 atop v0.2.10). Point at the sibling checkout
// so the Go protocol bindings are available while the feature is built across
// repos. RELEASE GATE: tag/publish types (>= v0.2.11) and bump the require
// above, then DROP this replace before tagging xll-gen.
replace github.com/xll-gen/types => ../types
