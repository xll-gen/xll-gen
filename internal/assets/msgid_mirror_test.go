package assets

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"testing"

	shm "github.com/xll-gen/shm/go"

	"github.com/xll-gen/xll-gen/pkg/msgid"
)

// msgidSourcePath is pkg/msgid/msgid.go relative to this package's directory
// (go test runs each package with its own directory as the working directory).
const msgidSourcePath = "../../pkg/msgid/msgid.go"

var (
	msgDefineRe = regexp.MustCompile(`(?m)^#define\s+(MSG_[A-Z0-9_]+)\s+(\d+)\s*$`)
	msgMirrorRe = regexp.MustCompile(`\bMSG_[A-Z0-9_]+\b`)
)

// goMsgIDs is the Go side of the message-ID mirror (AGENTS.md §18.6), keyed by
// the C++ #define name each constant mirrors.
//
// It is DERIVED from pkg/msgid/msgid.go by parsing it, not hand-copied here:
// a hand-maintained table is opt-in, so a new constant that nobody remembered
// to list simply would not be checked (proved: adding `MsgSneaky = 2` to
// pkg/msgid alone left the whole suite green while this was a literal map).
// The C++ mirror name comes from the constant's own doc comment — every
// pkg/msgid constant must name the MSG_* #define it mirrors there, which is
// what makes the derivation total rather than best-effort.
func goMsgIDs(t *testing.T) map[string]int {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.FromSlash(msgidSourcePath), nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", msgidSourcePath, err)
	}
	out := make(map[string]int)
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if !name.IsExported() {
					continue
				}
				if i >= len(vs.Values) {
					t.Fatalf("pkg/msgid: %s has no explicit value; every message ID must be a literal so this gate can read it", name.Name)
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.INT {
					t.Fatalf("pkg/msgid: %s is not an integer literal (%T); every message ID must be a literal so this gate can read it", name.Name, vs.Values[i])
				}
				v, err := strconv.Atoi(lit.Value)
				if err != nil {
					t.Fatalf("pkg/msgid: %s = %s: %v", name.Name, lit.Value, err)
				}
				doc := vs.Doc.Text()
				mirror := msgMirrorRe.FindString(doc)
				if mirror == "" {
					t.Fatalf("pkg/msgid: %s has no MSG_* mirror named in its doc comment. Every constant in "+
						"pkg/msgid is one half of the C++ mirror (AGENTS.md §18.6); say which #define in "+
						"include/xll_ipc.h it mirrors, or this gate cannot check it.", name.Name)
				}
				if prev, dup := out[mirror]; dup {
					t.Fatalf("pkg/msgid: %s claims to mirror %s, which is already claimed (= %d)", name.Name, mirror, prev)
				}
				out[mirror] = v
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("no constants parsed from %s — the file layout drifted", msgidSourcePath)
	}
	return out
}

// headerMsgIDs parses the MSG_* #defines out of the SHIPPED C++ header rather
// than trusting a comment or a second hand-copied table.
func headerMsgIDs(t *testing.T) map[string]int {
	t.Helper()
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	hdr, ok := m["include/xll_ipc.h"]
	if !ok {
		t.Fatal("include/xll_ipc.h missing from embedded assets")
	}
	out := make(map[string]int)
	for _, match := range msgDefineRe.FindAllStringSubmatch(hdr, -1) {
		v, err := strconv.Atoi(match[2])
		if err != nil {
			t.Fatalf("#define %s: %v", match[1], err)
		}
		out[match[1]] = v
	}
	if len(out) == 0 {
		t.Fatal("no MSG_* #defines parsed from include/xll_ipc.h — the regex or the header format drifted")
	}
	return out
}

// TestMessageIDMirrorMatchesHeader is the actual mirror gate for AGENTS.md
// §18.6. pkg/msgid/msgid_test.go only pins the Go values against a second
// hand-written copy of the same numbers, so it cannot notice the C++ side
// drifting; this reads the numbers out of the shipped xll_ipc.h and compares
// them name by name, in both directions.
func TestMessageIDMirrorMatchesHeader(t *testing.T) {
	hdr := headerMsgIDs(t)
	goIDs := goMsgIDs(t)

	for _, name := range sortedKeys(goIDs) {
		want := goIDs[name]
		got, ok := hdr[name]
		if !ok {
			t.Errorf("%s is defined in pkg/msgid but has no #define in include/xll_ipc.h", name)
			continue
		}
		if got != want {
			t.Errorf("%s: xll_ipc.h = %d, pkg/msgid = %d (the two mirrors must be byte-identical, AGENTS.md §18.6)", name, got, want)
		}
	}
	for _, name := range sortedKeys(hdr) {
		if _, ok := goIDs[name]; !ok {
			t.Errorf("#define %s = %d exists in include/xll_ipc.h but has no pkg/msgid twin (add a constant to pkg/msgid whose doc comment names %s)", name, hdr[name], name)
		}
	}
}

// reservedShmMsgTypes is every value shm's own MsgType enum claims below
// APP_START (shm/include/shm/IPCUtils.h). An application-layer ID that lands
// on one of these is ambiguous on the wire: a host that branches on the shm
// meaning (e.g. the heartbeat response at 2) mis-routes the application
// message.
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

// TestMessageIDsAreApplicationLevel is the gate that would have caught
// MsgAck = 2 colliding with shm's HEARTBEAT_RESP (found 2026-08-03).
//
// Every xll-gen message ID is an APPLICATION-layer ID: it is handed to shm as
// the slot's msgType (pkg/server's SendAckOrChunk and the generated dispatch
// switch both do this), so it shares the numbering space with shm's own
// MsgType enum. shm documents APP_START = 128 as the floor for application
// values; anything below it is reserved by the transport.
//
// Both mirrors are checked, so neither side can drift back into the reserved
// range on its own.
func TestMessageIDsAreApplicationLevel(t *testing.T) {
	const appStart = int(shm.MsgTypeAppStart)

	check := func(source, name string, v int) {
		t.Helper()
		if reserved, ok := reservedShmMsgTypes[v]; ok {
			t.Errorf("%s %s = %d collides with shm MsgType::%s — an application ID must not reuse a transport-reserved value", source, name, v, reserved)
		}
		if v < appStart {
			t.Errorf("%s %s = %d is below shm's documented application floor APP_START = %d", source, name, v, appStart)
		}
	}

	goIDs := goMsgIDs(t)
	for _, name := range sortedKeys(goIDs) {
		check("pkg/msgid", name, goIDs[name])
	}
	hdr := headerMsgIDs(t)
	for _, name := range sortedKeys(hdr) {
		check("xll_ipc.h", name, hdr[name])
	}
}

// TestMessageIDsAreUnique catches a renumber that lands on an already-taken
// slot — the failure mode this check exists to prevent is two handlers
// answering the same wire ID, which the dispatch switch resolves silently by
// first-case-wins (or, in the generated Go, refuses to compile).
func TestMessageIDsAreUnique(t *testing.T) {
	goIDs := goMsgIDs(t)
	seen := make(map[int]string)
	for _, name := range sortedKeys(goIDs) {
		v := goIDs[name]
		if prev, ok := seen[v]; ok {
			t.Errorf("%s and %s are both %d", prev, name, v)
			continue
		}
		seen[v] = name
	}
	// MSG_USER_START is a base, not a single ID: user function i occupies
	// MsgUserStart + i, so nothing else may sit at or above it.
	for _, name := range sortedKeys(goIDs) {
		if name == "MSG_USER_START" {
			continue
		}
		if goIDs[name] >= msgid.MsgUserStart {
			t.Errorf("%s = %d is at or above MSG_USER_START = %d, so it collides with user function %d",
				name, goIDs[name], msgid.MsgUserStart, goIDs[name]-msgid.MsgUserStart)
		}
	}
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
