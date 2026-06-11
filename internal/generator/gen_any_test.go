package generator

import (
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/config"
)

// anyFunctions returns one sync and one async function with `return: "any"`,
// in the normalized shape ApplyDefaults produces (Mode lowercase, Async flag
// synced with Mode).
func anyFunctions() []config.Function {
	return []config.Function{
		{
			Name:   "EchoAny",
			Return: "any",
			Args:   []config.Arg{{Name: "v", Type: "any"}},
			Mode:   "sync",
		},
		{
			Name:   "AsyncAny",
			Return: "any",
			Args:   []config.Arg{{Name: "v", Type: "any"}},
			Mode:   "async",
			Async:  true,
		},
	}
}

// TestGenGo_AnyReturn_Interface verifies that a `return: "any"` handler is
// declared with a plain Go `any` return (handlers cannot construct the
// *protocol.Any read view), while the argument keeps the *protocol.Any view.
func TestGenGo_AnyReturn_Interface(t *testing.T) {
	t.Parallel()

	data := struct {
		Package   string
		ModName   string
		Functions []config.Function
		Events    []config.Event
		Commands  []config.Command
		Version   string
		Rtd       config.RtdConfig
	}{
		Package:   "generated",
		ModName:   "testmod",
		Functions: anyFunctions(),
		Version:   "test",
	}

	iface := renderTemplate(t, "interface.go.tmpl", data)
	assertParses(t, "interface.go", iface)

	if !strings.Contains(iface, "EchoAny(ctx context.Context, v *protocol.Any) (any, error)") {
		t.Errorf("interface.go: sync any-return signature wrong:\n%s", iface)
	}
	if !strings.Contains(iface, "AsyncAny(ctx context.Context, v *protocol.Any) (any, error)") {
		t.Errorf("interface.go: async any-return signature wrong:\n%s", iface)
	}
}

// TestGenGo_AnyReturn_Server is the regression for the v0.4.x bug where the
// sync path emitted `ipc.EchoAnyResponseAddResult(b, res)` with res being a
// *protocol.Any — a type error (the setter takes flatbuffers.UOffsetT), so
// any-returning sync functions produced non-compiling servers. The fix
// serializes res through server.BuildAnyFromGo; the async path maps res
// through server.MapAnyValue before queueing.
func TestGenGo_AnyReturn_Server(t *testing.T) {
	t.Parallel()

	data := struct {
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
		Functions:   anyFunctions(),
		Version:     "test",
		Logging:     config.LoggingConfig{Level: "info", Dir: "logs"},
	}

	srv := renderTemplate(t, "server.go.tmpl", data)
	assertParses(t, "server.go", srv)

	// Sync path: result serialized via the canonical Go-value→Any builder and
	// attached as an offset.
	if !strings.Contains(srv, "resOffset = server.BuildAnyFromGo(b, res)") {
		t.Errorf("server.go: sync any-return must serialize via server.BuildAnyFromGo:\n%s", srv)
	}
	if !strings.Contains(srv, "ipc.EchoAnyResponseAddResult(b, resOffset)") {
		t.Errorf("server.go: sync any-return must add the serialized offset:\n%s", srv)
	}
	// The broken form: passing the handler result directly to the offset setter.
	if strings.Contains(srv, "ipc.EchoAnyResponseAddResult(b, res)") {
		t.Errorf("server.go: sync any-return still passes raw res to AddResult (v0.4.x bug):\n%s", srv)
	}

	// Async path: result mapped to (tag, payload) at queue time.
	if !strings.Contains(srv, "tag, payload := server.MapAnyValue(res)") {
		t.Errorf("server.go: async any-return must map via server.MapAnyValue:\n%s", srv)
	}
	if !strings.Contains(srv, "asyncBatcher.QueueResult(handle, payload, tag, \"\")") {
		t.Errorf("server.go: async any-return must queue the mapped value:\n%s", srv)
	}
}
