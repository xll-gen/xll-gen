package templates

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	shm "github.com/xll-gen/shm/go"
)

// msgTypeCastRe finds every `(shm::MsgType)` cast in a template and captures
// the BASE operand — the first token after the cast, looking through an
// optional `(` and an optional `{{add …}}` / `{{sub …}}` action.
//
// It deliberately captures the base rather than the whole expression: user
// function IDs are always written as base+index, and it is the base that
// decides whether the whole range lands in application space.
//
//	(shm::MsgType)140                  -> "140"
//	(shm::MsgType)({{add 11 $i}})      -> "11"
//	(shm::MsgType){{add MsgUserStart $i}} -> "MsgUserStart"
//
// A declaration like `shm::MsgType msgId` is not a cast and does not match:
// the parentheses around the type name are required.
var msgTypeCastRe = regexp.MustCompile(
	`\(shm::MsgType\)\s*\(?\s*(?:\{\{-?\s*(?:add|sub)\s+\(?\s*)?([A-Za-z_][A-Za-z0-9_]*|\d+)`)

// reservedShmMsgTypes names every value shm's transport enum claims below
// APP_START (shm/include/shm/IPCUtils.h), so the failure message can say WHICH
// transport message a literal would be confused with rather than just "too
// small".
var reservedShmMsgTypes = map[int]string{
	int(shm.MsgTypeNormal):        "NORMAL",
	int(shm.MsgTypeHeartbeatReq):  "HEARTBEAT_REQ",
	int(shm.MsgTypeHeartbeatResp): "HEARTBEAT_RESP",
	int(shm.MsgTypeShutdown):      "SHUTDOWN",
	int(shm.MsgTypeFlatbuffer):    "FLATBUFFER",
	int(shm.MsgTypeGuestCall):     "GUEST_CALL",
	int(shm.MsgTypeStreamStart):   "STREAM_START",
	int(shm.MsgTypeStreamChunk):   "STREAM_CHUNK",
	int(shm.MsgTypeSystemError):   "SYSTEM_ERROR",
}

// templateNames lists the .tmpl files on disk, which is exactly the set the
// `//go:embed *.tmpl` pattern ships. The CONTENT is then read back through
// Get(), so this gate reads the shipped bytes and not a stale working copy.
func templateNames(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read template dir: %v", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".tmpl" {
			out = append(out, e.Name())
		}
	}
	if len(out) == 0 {
		t.Fatal("no .tmpl files found — the package layout drifted and this gate is scanning nothing")
	}
	sort.Strings(out)
	return out
}

// TestTemplateMsgTypeCastsAreApplicationLevel is the gate that would have
// caught `(shm::MsgType)({{add 11 $i}})` in regtest_main.cpp.tmpl (found
// 2026-08-03): a user-function message ID based at 11, i.e. squarely inside
// shm's transport-reserved range (GUEST_CALL 11, STREAM_START 13, STREAM_CHUNK
// 14), while the product bases the same IDs at MsgUserStart = 140.
//
// The existing mirror gate, internal/assets/msgid_mirror_test.go, cannot see
// this. It reads the CONSTANTS — pkg/msgid/msgid.go and the MSG_* #defines in
// the shipped include/xll_ipc.h — and both of those were, and are, correct. A
// numeric literal typed directly into a template bypasses both mirrors
// entirely; the constants stay in perfect agreement while the emitted C++
// talks on a different wire. That structural blindness is exactly how the
// defect survived a gate written for it.
//
// So this gate reads the TEMPLATES, and it enforces the one property the
// numbering space guarantees (AGENTS.md §18.6): an application-layer ID is
// >= shm's APP_START. Anything below it is the transport's.
//
// Symbolic bases (MsgUserStart, funcmap helpers, C++ enumerators) are the
// intended way to write these and are accepted without inspection — their
// values are already pinned by the mirror gate. This test is only about raw
// numbers, which nothing else covers.
func TestTemplateMsgTypeCastsAreApplicationLevel(t *testing.T) {
	appStart := int(shm.MsgTypeAppStart)

	totalCasts := 0
	for _, name := range templateNames(t) {
		content, err := Get(name)
		if err != nil {
			t.Fatalf("Get(%s): %v", name, err)
		}
		lines := strings.Split(content, "\n")
		for lineNo, line := range lines {
			for _, m := range msgTypeCastRe.FindAllStringSubmatch(line, -1) {
				totalCasts++
				base := m[1]
				v, err := strconv.Atoi(base)
				if err != nil {
					// Symbolic base — MsgUserStart and friends. Pinned elsewhere.
					continue
				}
				if reserved, ok := reservedShmMsgTypes[v]; ok {
					t.Errorf("%s:%d casts a raw literal to shm::MsgType with base %d, which IS shm::MsgType::%s — a transport-reserved value. Message IDs are application-layer and must be written against MsgUserStart (or another pkg/msgid constant), never as a number (AGENTS.md §18.6).\n    %s",
						name, lineNo+1, v, reserved, strings.TrimSpace(line))
					continue
				}
				if v < appStart {
					t.Errorf("%s:%d casts a raw literal to shm::MsgType with base %d, below shm's application floor APP_START = %d. Message IDs are application-layer and must be written against MsgUserStart (or another pkg/msgid constant), never as a number (AGENTS.md §18.6).\n    %s",
						name, lineNo+1, v, appStart, strings.TrimSpace(line))
				}
			}
		}
	}

	if totalCasts == 0 {
		t.Fatal("no (shm::MsgType) casts found in any template — the emitted call shape drifted and this gate is now matching nothing; fix msgTypeCastRe rather than deleting the test")
	}
}
