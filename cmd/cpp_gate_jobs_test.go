package cmd

import (
	"os"
	"runtime"
	"strconv"
)

// cppGateBuildJobs is the -j value handed to `cmake --build --parallel` by every
// C++ compile gate.
//
// WHY THIS EXISTS. The gates generate with "MinGW Makefiles", whose build is
// SERIAL unless a job count is given, so each gate compiled one translation unit
// at a time -- the generated XLL plus flatbuffers, zstd and types, from scratch,
// in its own build tree. Measured on a 24-core machine before this existed, the
// five gates were 162 + 114 + 56 + 51 + 51 = 433s of a 470s `go test ./cmd/`.
// The FetchContent cache the gates share (FETCHCONTENT_BASE_DIR) only skips the
// DOWNLOAD; every gate still builds those dependencies itself, which is what the
// wall clock was actually going to.
//
// The gates stay serial WITH RESPECT TO EACH OTHER (see the "deliberately NOT
// t.Parallel" notes) because they contend on that shared cache dir. Parallelism
// therefore has to come from inside each build, which is exactly what this
// provides.
//
// Capped rather than NumCPU: these gates run alongside `go test` itself and, on
// a developer machine, alongside everything else. Oversubscribing a 24-core box
// with 24 compiler processes per gate buys nothing over ~8 for a handful of TUs
// and makes the machine unusable while the suite runs. XLLGEN_GATE_JOBS
// overrides it for a CI box that wants the whole machine.
func cppGateBuildJobs() string {
	if v := os.Getenv("XLLGEN_GATE_JOBS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return strconv.Itoa(n)
		}
	}
	n := runtime.NumCPU()
	if n > 8 {
		n = 8
	}
	if n < 1 {
		n = 1
	}
	return strconv.Itoa(n)
}
