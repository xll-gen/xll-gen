package assets

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/pkg/server"
)

// ---------------------------------------------------------------------------
// The shared accept/reject table
// ---------------------------------------------------------------------------
//
// One table, two consumers:
//
//   - TestChunkSegmentCasesMatchGoClaimSegment replays it against the GO
//     reassembler (pkg/server: HandleChunk's bounds + zero-length guards, then
//     ChunkBuffer.ClaimSegment);
//   - TestChunkSegmentNativeBehavior emits it as C++ and replays it against the
//     C++ reassembler (xll::ClaimChunkSegment, internal/assets/files/include/
//     xll_worker.h) through testdata/chunk_segments_native_test.cpp.
//
// Sharing the table is the point. AGENTS.md §18.6.1 calls the two reassemblers
// "a mirror of RULES, not a shared mechanism" — they run in opposite directions
// and share nothing at runtime, so the only thing that can keep them aligned is
// a single specification both are checked against. Two hand-written case lists
// would drift; this one cannot.
//
// Deliberate NON-mirror, do not "fix" it here: total-size mismatch on transfer
// id reuse (Go replaces the buffer, C++ refuses and poisons). That divergence
// is above the segment layer and is documented as intentional in AGENTS.md
// §18.6.1 — it is not expressible in this table and must not be added to it
// without the cross-repo decision that section calls for.

// segStep is one arriving chunk range plus the verdict BOTH reassemblers must
// produce for it.
type segStep struct {
	Off, Len uint32
	// Want is one of:
	//   "new"     — disjoint, recorded, caller copies + advances
	//   "dup"     — exact retransmit, caller skips copy AND advance
	//   "overlap" — protocol violation, drop + poison the transfer
	//   "zero"    — present-but-empty payload, drop + poison
	//   "oob"     — range does not fit in [0, total), drop + poison
	Want string
}

// segCase is one transfer: a declared total plus the ordered arrivals.
type segCase struct {
	Name  string
	Total uint32
	Steps []segStep
}

var chunkSegmentCases = []segCase{
	// --- the happy shapes ------------------------------------------------
	{
		Name: "DisjointAscending", Total: 1000,
		Steps: []segStep{{0, 10, "new"}, {10, 10, "new"}, {20, 10, "new"}, {30, 10, "new"}},
	},
	{
		// Out-of-order arrival is legal: neither side requires monotonic
		// offsets, only disjointness. Mirrors Go's
		// TestChunkBuffer_ClaimSegment/DisjointDescending_SortedInsert.
		Name: "DisjointDescendingArrival", Total: 1000,
		Steps: []segStep{{30, 10, "new"}, {20, 10, "new"}, {10, 10, "new"}, {0, 10, "new"}},
	},
	{
		// ADJACENT (touching, non-overlapping) must be ACCEPTED. This is the
		// case an off-by-one in either neighbour comparison breaks, and
		// breaking it makes every normal multi-chunk transfer fail.
		Name: "ExactlyAbuttingIsNew", Total: 1000,
		Steps: []segStep{{0, 50, "new"}, {100, 50, "new"}, {50, 50, "new"}},
	},
	{
		Name: "AbuttingAtBothEnds", Total: 30,
		Steps: []segStep{{10, 10, "new"}, {0, 10, "new"}, {20, 10, "new"}},
	},

	// --- retransmit vs. re-chunking --------------------------------------
	{
		Name: "ExactRepeatIsDuplicate", Total: 1000,
		Steps: []segStep{{100, 50, "new"}, {100, 50, "dup"}, {100, 50, "dup"}},
	},
	{
		// Same start offset, DIFFERENT length: a producer that re-chunked
		// mid-transfer. Silently changing which bytes are covered is exactly
		// what the overlap rule exists to catch, so this is a violation, not a
		// retransmit.
		Name: "SameOffsetShorterIsOverlap", Total: 1000,
		Steps: []segStep{{100, 50, "new"}, {100, 20, "overlap"}},
	},
	{
		Name: "SameOffsetLongerIsOverlap", Total: 1000,
		Steps: []segStep{{100, 50, "new"}, {100, 80, "overlap"}},
	},

	// --- the four overlap geometries -------------------------------------
	{
		// Arriving range starts inside the PREDECESSOR's tail.
		Name: "OverlapsPredecessorTail", Total: 1000,
		Steps: []segStep{{0, 60, "new"}, {50, 40, "overlap"}},
	},
	{
		// Arriving range's tail runs into the SUCCESSOR's head.
		Name: "OverlapsSuccessorHead", Total: 1000,
		Steps: []segStep{{50, 40, "new"}, {0, 60, "overlap"}},
	},
	{
		Name: "FullyContainedIsOverlap", Total: 1000,
		Steps: []segStep{{0, 100, "new"}, {10, 10, "overlap"}},
	},
	{
		Name: "FullyContainingIsOverlap", Total: 1000,
		Steps: []segStep{{40, 10, "new"}, {0, 100, "overlap"}},
	},
	{
		// The single-byte overshoot on each side: the smallest input that
		// distinguishes ">" from ">=" in the two neighbour comparisons.
		Name: "OneByteOvershootPredecessor", Total: 1000,
		Steps: []segStep{{0, 51, "new"}, {50, 10, "overlap"}},
	},
	{
		Name: "OneByteOvershootSuccessor", Total: 1000,
		Steps: []segStep{{50, 10, "new"}, {40, 11, "overlap"}},
	},
	{
		// The historical bug, verbatim (AGENTS.md §18.6.1 / xll_worker.cpp):
		// total=100 with (0,60) then (50,40) sums to 100 under an offset-only
		// dedup and reported COMPLETE while [90,100) was never written.
		Name: "SumsToTotalButLeavesAGap", Total: 100,
		Steps: []segStep{{0, 60, "new"}, {50, 40, "overlap"}},
	},

	// --- a rejected range must not disturb what was already accepted ------
	{
		Name: "RejectLeavesEarlierCoverageIntact", Total: 100,
		Steps: []segStep{{0, 40, "new"}, {30, 40, "overlap"}, {40, 60, "new"}},
	},

	// --- zero length ------------------------------------------------------
	{
		// A present-but-empty payload advances nothing, so it can never be part
		// of a valid transfer — and recording an (off, 0) range would make the
		// REAL chunk at that offset classify as overlap and kill the transfer.
		Name: "ZeroLengthRefusedOnEmptyMap", Total: 100,
		Steps: []segStep{{0, 0, "zero"}},
	},
	{
		Name: "ZeroLengthRefusedMidTransfer", Total: 100,
		Steps: []segStep{{0, 40, "new"}, {40, 0, "zero"}, {40, 60, "new"}},
	},
	{
		// At the very end of the buffer the bounds check passes (offset ==
		// total, length 0), so this reaches the zero-length rule specifically.
		Name: "ZeroLengthAtTotalIsZeroNotOob", Total: 100,
		Steps: []segStep{{100, 0, "zero"}},
	},

	// --- bounds -----------------------------------------------------------
	{
		Name: "OffsetPastTotalIsOob", Total: 100,
		Steps: []segStep{{101, 1, "oob"}},
	},
	{
		Name: "TailPastTotalIsOob", Total: 100,
		Steps: []segStep{{90, 11, "oob"}},
	},
	{
		Name: "ExactlyFillingTheBufferIsNew", Total: 100,
		Steps: []segStep{{0, 100, "new"}},
	},
	{
		Name: "LastByteIsNew", Total: 100,
		Steps: []segStep{{99, 1, "new"}},
	},
	{
		// uint32 BOUNDARY. offset+length = 0x100000010 wraps to 0x10 in 32-bit
		// arithmetic, so the additive bounds form would accept a range that
		// writes 0x20 bytes starting 0x10 bytes before the end of a
		// 0xFFFFFFFF-byte buffer. Both sides must compute the comparison wide
		// (Go: uint64; C++: the subtraction form) and refuse.
		Name: "Uint32SumWrapIsOob", Total: 0xFFFFFFFF,
		Steps: []segStep{{0xFFFFFFF0, 0x20, "oob"}},
	},
	{
		// The same neighbourhood, well-formed: the last 16 bytes of a
		// 0xFFFFFFFF buffer must still be accepted, so the wrap guard is not
		// just "refuse everything near the ceiling".
		Name: "Uint32CeilingWellFormedIsNew", Total: 0xFFFFFFFF,
		Steps: []segStep{{0xFFFFFFEF, 0x10, "new"}},
	},
	{
		// Neighbour comparisons at the ceiling: prev.Offset+prev.Length must be
		// computed wide too, or 0xFFFFFFEF+0x10 wraps to 0xFFFFFFFF and the
		// abutting range below it is misjudged.
		Name: "Uint32CeilingNeighbourArithmetic", Total: 0xFFFFFFFF,
		Steps: []segStep{
			{0xFFFFFFEF, 0x10, "new"},
			{0xFFFFFFE0, 0x0F, "new"},
			{0xFFFFFFE0, 0x10, "overlap"},
		},
	},
}

// ---------------------------------------------------------------------------
// Go side of the shared table
// ---------------------------------------------------------------------------

// goClaim replays one step through the GO reassembler, in the exact order
// pkg/server/handlers.go::HandleChunk applies its rules: bounds check first
// (against len(buf.Data) == TotalSize), then the zero-length refusal, then
// ChunkBuffer.ClaimSegment.
//
// The two guards are re-stated here rather than reached through HandleChunk
// because HandleChunk needs a full FlatBuffers frame, an shm response buffer
// and a dispatch closure to reach three lines of arithmetic. The wire-level
// coverage of the same guards lives in pkg/server/manager_test.go; what this
// function is for is checking that the RULE SET the C++ side implements is the
// Go rule set.
//
// buf carries no Data slice on purpose: ClaimSegment never touches it, and the
// uint32-ceiling cases would otherwise demand a 4 GiB allocation.
func goClaim(buf *server.ChunkBuffer, total uint32, s segStep) string {
	if uint64(s.Off)+uint64(s.Len) > uint64(total) {
		return "oob"
	}
	if s.Len == 0 {
		return "zero"
	}
	switch buf.ClaimSegment(s.Off, s.Len) {
	case server.ClaimNew:
		return "new"
	case server.ClaimDuplicate:
		return "dup"
	case server.ClaimOverlap:
		return "overlap"
	}
	return "?"
}

// TestChunkSegmentCasesMatchGoClaimSegment is half of the mirror gate: it
// proves the shared table describes the GO reassembler's real behavior. The C++
// gate below then holds xll::ClaimChunkSegment to the same table, so the two
// together are a mirror check rather than two independent test suites that
// happen to look similar.
func TestChunkSegmentCasesMatchGoClaimSegment(t *testing.T) {
	for _, c := range chunkSegmentCases {
		t.Run(c.Name, func(t *testing.T) {
			buf := &server.ChunkBuffer{TotalSize: int(c.Total)}
			var received uint64
			for i, s := range c.Steps {
				before := len(buf.Segments)
				got := goClaim(buf, c.Total, s)
				if got != s.Want {
					t.Fatalf("step %d (off=%d len=%d total=%d): Go verdict %q, table says %q",
						i, s.Off, s.Len, c.Total, got, s.Want)
				}
				if got == "new" {
					received += uint64(s.Len)
					if len(buf.Segments) != before+1 {
						t.Fatalf("step %d: an accepted range must be recorded exactly once", i)
					}
				} else if len(buf.Segments) != before {
					t.Fatalf("step %d: a %q verdict must leave Segments unchanged", i, got)
				}
			}
			// Ascending + disjoint + Σ == Received: the invariants the
			// `Received == TotalSize` completion test relies on.
			var sum uint64
			var prevEnd uint64
			for i, sg := range buf.Segments {
				if i > 0 && uint64(sg.Offset) < prevEnd {
					t.Fatalf("Segments not disjoint/ascending: %v", buf.Segments)
				}
				prevEnd = uint64(sg.Offset) + uint64(sg.Length)
				if prevEnd > uint64(c.Total) {
					t.Fatalf("segment %v runs past total %d", sg, c.Total)
				}
				sum += uint64(sg.Length)
			}
			if sum != received {
				t.Fatalf("Sum(segment lengths)=%d, caller-side Received=%d", sum, received)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Always-on source marker (no toolchain required)
// ---------------------------------------------------------------------------

// TestChunkSegmentLogicIsExtracted pins that HandleChunk still ROUTES through
// the pure helper instead of re-inlining the classification.
//
// This is what makes the offline gate meaningful: if someone pastes the
// lower_bound arbitration back into xll_worker.cpp, the C++ gate below would
// keep passing (it tests the header) while the shipped reassembler quietly
// stopped being the thing under test. Cheap always-on guard, in the same spirit
// as TestCacheMapsUseRealLocks.
func TestChunkSegmentLogicIsExtracted(t *testing.T) {
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	hdr, ok := m["include/xll_worker.h"]
	if !ok {
		t.Fatalf("embedded include/xll_worker.h not found in assets")
	}
	src, ok := m["src/xll_worker.cpp"]
	if !ok {
		t.Fatalf("embedded src/xll_worker.cpp not found in assets")
	}
	code := stripLineComments(src)

	for _, want := range []string{
		"enum class ChunkSegmentClaim",
		"inline ChunkSegmentClaim ClaimChunkSegment(std::map<uint32_t, uint32_t>& segments,",
	} {
		if !strings.Contains(hdr, want) {
			t.Errorf("xll_worker.h must keep the pure segment arbiter (%q): it is what lets "+
				"internal/assets/testdata/chunk_segments_native_test.cpp exercise the rules "+
				"without Excel/FlatBuffers/shm", want)
		}
	}
	if !strings.Contains(code, "ClaimChunkSegment(") {
		t.Errorf("HandleChunk no longer calls ClaimChunkSegment: the offline gate would still " +
			"pass while the shipped reassembler runs untested code")
	}
	// The re-inlined shape, specifically.
	if strings.Contains(code, "pm.receivedSegments.lower_bound(") {
		t.Errorf("xll_worker.cpp arbitrates overlap inline again (pm.receivedSegments.lower_bound); " +
			"the classification must stay in ClaimChunkSegment so it can be unit-tested offline")
	}
	if !strings.Contains(code, "pm.receivedSize += len;") {
		t.Errorf("HandleChunk must still advance receivedSize on the accepted-range path only")
	}
}

// ---------------------------------------------------------------------------
// Compile + run gate
// ---------------------------------------------------------------------------

// emitCppCases renders the shared table as the C++ initializer the harness
// includes. Keeping the emitter here (rather than checking a generated file in)
// is what guarantees the two sides cannot drift: there is no committed copy to
// forget to regenerate.
func emitCppCases(cases []segCase) string {
	var b strings.Builder
	b.WriteString("// GENERATED by internal/assets/chunk_cpp_test.go — do not edit.\n")
	b.WriteString("// Source of truth: chunkSegmentCases in that file, which is also replayed\n")
	b.WriteString("// against pkg/server's Go reassembler by TestChunkSegmentCasesMatchGoClaimSegment.\n")
	b.WriteString("const std::vector<Case> kCases = {\n")
	for _, c := range cases {
		fmt.Fprintf(&b, "    {%q, %du, {", c.Name, c.Total)
		for i, s := range c.Steps {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "{%du, %du, %q}", s.Off, s.Len, s.Want)
		}
		b.WriteString("}},\n")
	}
	b.WriteString("};\n")
	return b.String()
}

// TestChunkSegmentNativeBehavior compiles the EMBEDDED xll_worker.h together
// with testdata/chunk_segments_native_test.cpp and runs it.
//
// Why this gate exists (2026-07-26 backlog, MED): the Go host->guest
// reassembler is covered by TestChunkBuffer_ClaimSegment plus wire-path tests,
// and regtest's mock_host cases 16a..17e exercise the GO guest — but the C++
// guest->host mirror in xll_worker.cpp had no automated test whatsoever. The
// cmake compile gates only ever proved it compiles, so the 2026-07-26 chunk
// hardening (zero-length refusal, poison set, final rejection) shipped on a
// compile gate plus review. This is the missing offline unit gate, modelled on
// TestCacheNativeBehavior.
//
// Unlike that gate it needs NOTHING but g++: the unit under test is a pure
// function over std::map, so there are no types/flatbuffers/phmap headers to
// locate and no FetchContent cache to prime. It is still skipped under -short
// and off Windows, matching its siblings (the header is part of a Windows-only
// asset tree, and keeping the skip rule uniform avoids a gate that behaves
// differently per platform).
func TestChunkSegmentNativeBehavior(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping C++ compile+run gate in short mode")
	}
	if runtime.GOOS != "windows" {
		t.Skip("xll-gen's C++ assets are Windows-only (AGENTS.md §0.1)")
	}

	gxx, err := exec.LookPath("g++")
	if err != nil {
		t.Skip("g++ not on PATH; skipping chunk-segment compile+run gate")
	}

	_, thisFile, ok := callerFile()
	if !ok {
		t.Fatal("runtime.Caller failed")
	}

	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	hdr, ok := m["include/xll_worker.h"]
	if !ok {
		t.Fatalf("embedded include/xll_worker.h not found in assets")
	}

	dir := t.TempDir()
	incDir := filepath.Join(dir, "include")
	if err := os.MkdirAll(incDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Flat includes, like the generated layout (AGENTS.md §16.3).
	if err := os.WriteFile(filepath.Join(incDir, "xll_worker.h"), []byte(hdr), 0o644); err != nil {
		t.Fatal(err)
	}
	// The generated case table lands next to the harness's include path.
	if err := os.WriteFile(filepath.Join(incDir, "chunk_segment_cases.inc"),
		[]byte(emitCppCases(chunkSegmentCases)), 0o644); err != nil {
		t.Fatal(err)
	}

	harness := filepath.Join(filepath.Dir(thisFile), "testdata", "chunk_segments_native_test.cpp")
	if _, err := os.Stat(harness); err != nil {
		t.Fatalf("harness %s missing: %v", harness, err)
	}

	exePath := filepath.Join(dir, "chunk_segments_native_test.exe")
	args := []string{
		// gnu++17 mirrors the real build (CMakeLists.txt.tmpl); -Wall -Wextra
		// -Werror is affordable here because the translation unit is the header
		// under test plus the harness, with no third-party headers to placate.
		"-std=gnu++17", "-O2", "-Wall", "-Wextra", "-Werror",
		"-I", incDir,
		"-o", exePath,
		harness,
	}
	if out, err := exec.Command(gxx, args...).CombinedOutput(); err != nil {
		t.Fatalf("chunk-segment native harness failed to compile: %v\n%s", err, out)
	}

	out, err := exec.Command(exePath).CombinedOutput()
	t.Logf("chunk_segments_native_test output:\n%s", out)
	if err != nil {
		t.Fatalf("chunk-segment native harness reported failures (or crashed): %v", err)
	}
	if !strings.Contains(string(out), "0 failures") {
		t.Fatalf("chunk-segment native harness did not report 0 failures")
	}
}
