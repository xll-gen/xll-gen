package generator

import (
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
)

// rtdDateData builds the flat server.go.tmpl data struct for an RTD-backed
// function that takes a date arg. Mode is caller-supplied (rtd / rtd-once).
func rtdDateData(mode, ret string) any {
	return struct {
		Package       string
		ModName       string
		ProjectName   string
		Functions     []config.Function
		Events        []config.Event
		Commands      []config.Command
		ServerTimeout string
		ServerWorkers int
		Version       string
		Logging       config.LoggingConfig
		Rtd           config.RtdConfig
		Chunk         *config.ChunkConfig
	}{
		Package:     "generated",
		ModName:     "testmod",
		ProjectName: "TestProj",
		Functions: []config.Function{{
			Name:   "DateTick",
			Mode:   mode,
			Return: ret,
			Args:   []config.Arg{{Name: "asof", Type: "date"}, {Name: "sym", Type: "string"}},
		}},
		Version: "test",
		Logging: config.LoggingConfig{Level: "info", Dir: "logs"},
		Rtd:     config.RtdConfig{Enabled: true, ProgID: "TestProj.RTD"},
	}
}

// TestGen_RtdDateArgDispatch pins Defect C (Go side): an rtd / rtd-once function
// with a date arg must decode the topic string back to a time.Time via
// server.SerialToTime(server.ParseFloat(...)). Pre-fix, date fell through the
// dispatch type-ladder to the final else branch and passed the raw topic
// STRING to the handler's time.Time parameter -> generated Go compile failure.
func TestGen_RtdDateArgDispatch(t *testing.T) {
	t.Parallel()
	// rtd: date is arg 0 (topic index 1), string is arg 1 (topic index 2).
	rtd := renderTemplate(t, "server.go.tmpl", rtdDateData("rtd", "float"))
	assertParses(t, "server.go", rtd)
	want := "server.SerialToTime(server.ParseFloat(args[1]))"
	if !strings.Contains(rtd, want) {
		t.Errorf("rtd dispatch missing date decode %q:\n%s", want, rtd)
	}

	// rtd-once (scalar return) routes the same args through rtd.RunOnce.
	once := renderTemplate(t, "server.go.tmpl", rtdDateData("rtd-once", "float"))
	assertParses(t, "server.go", once)
	if !strings.Contains(once, want) {
		t.Errorf("rtd-once dispatch missing date decode %q:\n%s", want, once)
	}
}

// TestGenCpp_RtdDateArgTopic pins Defect C (C++ side): an rtd / rtd-once date
// arg (ArgCppType "double", passed by value) must be serialized into its RTD
// topic string as a plain scalar, NOT routed through the composite content-hash
// path (ContentHashToken), which takes an XLOPER12* and does not compile for a
// double. The scalar serialization must use the %.17g round-trip helper
// (FormatDoubleRoundTrip), NOT std::to_wstring — %f truncation would collide
// distinct serials (incl. the time-of-day fraction) onto one topic/once-key.
func TestGenCpp_RtdDateArgTopic(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"rtd", "rtd-once"} {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{
				Project: config.ProjectConfig{Name: "TestProj", Version: "0.1"},
				Rtd:     config.RtdConfig{Enabled: true, ProgID: "TestProj.RTD"},
				Functions: []config.Function{{
					Name:   "DateTick",
					Mode:   mode,
					Return: "float",
					Args:   []config.Arg{{Name: "asof", Type: "date"}, {Name: "sym", Type: "string"}},
				}},
				Server: config.ServerConfig{Timeout: "2s", Launch: &config.LaunchConfig{Enabled: new(bool)}},
			}
			content := renderCppMain(t, cfg)
			// The date arg must be stringified as a scalar serial with round-trip
			// precision.
			if !strings.Contains(content, "FormatDoubleRoundTrip(asof)") {
				t.Errorf("%s: date topic must be FormatDoubleRoundTrip(asof):\n%s", mode, content)
			}
			// The lossy %f path (std::to_wstring on a double) must be gone.
			if strings.Contains(content, "std::to_wstring(asof)") {
				t.Errorf("%s: date topic must not use lossy std::to_wstring:\n%s", mode, content)
			}
			// It must NOT fall into the composite content-hash branch.
			if strings.Contains(content, "ContentHashToken('a', asof)") {
				t.Errorf("%s: date arg wrongly routed through composite ContentHashToken:\n%s", mode, content)
			}
		})
	}
}
