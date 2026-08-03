package regtest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	shm "github.com/xll-gen/shm/go"

	"github.com/xll-gen/xll-gen/internal/config"
	"github.com/xll-gen/xll-gen/pkg/msgid"
)

// simSendRe captures the message ID the rendered simulation host writes into
// the slot header. It matches the RENDERED C++, so a template that computes the
// ID from the wrong base cannot hide behind the template action.
var simSendRe = regexp.MustCompile(`slot\.Send\([^;]*?\(shm::MsgType\)\s*\(?\s*(\d+)`)

// TestGenerateSimMainSendsMsgUserStartIDs pins the WIRE the `xll-gen regtest`
// simulation host talks on.
//
// Why this test exists at all (2026-08-03): the simulation host emitted
// `(shm::MsgType)(11 + i)` while the product — the generated XLL
// (`xll_main.cpp.tmpl`) and the generated Go dispatch switch
// (`server.go.tmpl`) — has used `MsgUserStart + i` = 140 + i since
// MSG_USER_START moved. The simulator and the product therefore disagreed
// about the protocol, and 11/13/14 are additionally TRANSPORT-RESERVED
// (`shm::MsgType::GUEST_CALL` / `STREAM_START` / `STREAM_CHUNK`, all below
// `APP_START` = 128, shm/include/shm/IPCUtils.h).
//
// Nothing noticed, because NOTHING RENDERS THIS TEMPLATE in the test suite:
// cmd/regression_test.go::TestRegression writes the hand-written fixture
// `internal/regtest/testdata/mock_host.cpp` (embedded as regtest.MockHostCpp)
// as the simulation main.cpp, and that fixture hardcodes 140, 141, 142 …
// (AGENTS.md §18.5). `regtest_main.cpp.tmpl` is reachable only through
// `regtest.Run()`, i.e. the `xll-gen regtest` subcommand, which is behind
// `//go:build regtest` and is built by nothing in the suite. TestRegression is
// green no matter what this template says — it is not a gate on it.
//
// This test is that gate: it renders the template and asserts on the bytes.
func TestGenerateSimMainSendsMsgUserStartIDs(t *testing.T) {
	cfg := &config.Config{}
	cfg.Project.Name = "simproj"
	cfg.Functions = []config.Function{
		{Name: "Alpha", Return: "int"},
		{Name: "Beta", Return: "int"},
		{Name: "Gamma", Return: "int"},
	}

	dir := t.TempDir()
	if err := generateSimMain(cfg, dir); err != nil {
		t.Fatalf("generateSimMain: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "main.cpp"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	matches := simSendRe.FindAllStringSubmatch(content, -1)
	if len(matches) != len(cfg.Functions) {
		t.Fatalf("found %d slot.Send message IDs in the rendered simulation host, want %d (one per function) — the emitted call shape drifted away from what this gate can read:\n%s",
			len(matches), len(cfg.Functions), content)
	}

	appStart := int(shm.MsgTypeAppStart)
	for i, m := range matches {
		got, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("message ID %q at index %d: %v", m[1], i, err)
		}
		want := msgid.MsgUserStart + i
		if got != want {
			t.Errorf("function %d (%s): simulation host sends msgType %d, but the generated XLL sends and the generated Go dispatch switches on MsgUserStart+%d = %d — the simulator and the product are on different wires (AGENTS.md §18.6)",
				i, cfg.Functions[i].Name, got, i, want)
		}
		if got < appStart {
			t.Errorf("function %d (%s): msgType %d is below shm's application floor APP_START = %d, so it collides with a transport-reserved shm::MsgType",
				i, cfg.Functions[i].Name, got, appStart)
		}
	}

	// Belt and braces: the exact literal the product emits must be present, so
	// a future edit cannot satisfy the loop above with a coincidence.
	for i := range cfg.Functions {
		lit := fmt.Sprintf("(shm::MsgType)(%d)", msgid.MsgUserStart+i)
		if !regexp.MustCompile(regexp.QuoteMeta(lit)).MatchString(content) {
			t.Errorf("rendered simulation host does not contain %s", lit)
		}
	}
}
