package generator

import (
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
)

// srvChunkData mirrors generateServer's anonymous struct closely enough to
// render server.go.tmpl's ChunkManager construction. Only the fields the
// template actually reads for that block need to be meaningful.
type srvChunkData struct {
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
}

func newSrvChunkData(chunk *config.ChunkConfig) srvChunkData {
	return srvChunkData{
		Package:       "generated",
		ModName:       "chunkcfg",
		ProjectName:   "chunkcfg",
		ServerTimeout: "10s",
		ServerWorkers: 0,
		Version:       "v0.0.0-test",
		Chunk:         chunk,
	}
}

// TestServerTmpl_ChunkConfigWiring pins the xll.yaml -> ChunkManagerConfig
// wiring for the `server.chunk` block (AGENTS.md §18.7: config fields and the
// templates that consume them are one co-change cluster). Every knob on
// config.ChunkConfig must reach server.ChunkManagerConfig, otherwise the knob
// parses, validates, documents — and silently does nothing.
//
// max_concurrent_transfers was the knob this test was added for: it is the
// aggregate-footprint bound (max_buffer_bytes only caps ONE transfer), so a
// deployment that lowers it and gets no effect would believe it is protected
// when it is not.
func TestServerTmpl_ChunkConfigWiring(t *testing.T) {
	t.Run("AllKnobsThreaded", func(t *testing.T) {
		out := renderTemplate(t, "server.go.tmpl", newSrvChunkData(&config.ChunkConfig{
			MaxBufferBytes:         134217728,
			CleanupInterval:        "15s",
			BufferTTL:              "45s",
			MaxConcurrentTransfers: 512,
		}))
		assertParses(t, "server.go", out)

		for _, want := range []string{
			"server.NewChunkManagerFromConfig(server.ChunkManagerConfig{",
			// Key alignment is part of the expectation: the pipeline does NOT
			// gofmt generated Go (golden_test.go), so an unaligned literal in
			// the template ships unaligned to every user project.
			"MaxBufferBytes:         134217728,",
			"CleanupInterval:        time.Duration(15000000000)",
			"BufferTTL:              time.Duration(45000000000)",
			"MaxConcurrentTransfers: 512,",
		} {
			if !strings.Contains(out, want) {
				t.Fatalf("rendered server.go missing %q\n---\n%s", want, out)
			}
		}
	})

	t.Run("OmittedBlockKeepsDefaults", func(t *testing.T) {
		out := renderTemplate(t, "server.go.tmpl", newSrvChunkData(nil))
		assertParses(t, "server.go", out)

		if !strings.Contains(out, "server.NewChunkManager()") {
			t.Fatalf("without server.chunk the template must use the plain constructor\n---\n%s", out)
		}
		if strings.Contains(out, "ChunkManagerConfig{") {
			t.Fatalf("without server.chunk the template must not emit a config literal\n---\n%s", out)
		}
	})
}
