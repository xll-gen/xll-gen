package assets

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xll-gen/xll-gen/pkg/chunk"
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
// The shared TRANSFER-BOOKKEEPING table
// ---------------------------------------------------------------------------
//
// The segment table above covers what happens to one arriving RANGE. This one
// covers the layer around it — which transfers may be OPENED at all, and how a
// refused one is remembered:
//
//   - total_size == 0 refused,
//   - total_size over the per-transfer cap refused,
//   - the concurrent-transfer bound, with PRUNE-THEN-REFUSE reclaim,
//   - the poison set: refuse-until-TTL, expiry, its own bound, and
//     oldest-eviction at that bound.
//
// Same discipline as the segment table: ONE table, replayed against Go's
// ChunkManager (TestChunkRegistryCasesMatchGoChunkManager) and emitted as C++
// for xll::ChunkRegistry (TestChunkRegistryNativeBehavior). Before the
// extraction all of this lived in xll_worker.cpp, which cannot be linked
// offline, so the whole layer was compile-checked only.
//
// The CAPS ARE PER-CASE, deliberately: both sides take them as parameters
// (ChunkManagerConfig / the ChunkRegistry constructor), so the bounds can be
// driven with three-entry maps instead of the shipped 1024. The shipped values
// are asserted separately — TestChunkReceiverCapsMatchGoConstants for the
// numbers, and the native harness for the registry's defaults.
//
// TIME IS LOGICAL. Every step carries a millisecond offset; the Go side injects
// it through ChunkManagerConfig.Clock and the C++ side passes it as `now`.
// Nothing sleeps, and TTL boundaries are exercised exactly (at the TTL = still
// refused; one tick past = expired).
//
// DELIBERATE NON-MIRRORS, do not "fix" them by adding them here:
//
//  1. TOTAL-SIZE MISMATCH ON ID REUSE — Go replaces the buffer in place, C++
//     keeps the first total and lets the re-open fail its bounds check
//     (AGENTS.md §18.6.1). Pinned per-side instead: Go's
//     TestChunkManager_TotalMismatchResetsBuffer, C++'s
//     TestExistingEntryKeepsTheFirstTotal in the native harness.
//  2. CAP VALIDATION ON AN ALREADY-OPEN ID — Go's GetChunkBuffer validates
//     `total` BEFORE looking the id up, so a chunk that declares total 0 (or a
//     total over the cap) for an id that is already open is REFUSED there,
//     while C++'s Admit returns Existing without re-validating. Same
//     "first total wins" family as (1), and only reachable from a producer that
//     changes total_size mid-transfer. Every case below therefore keeps a
//     transfer's declared total constant for its lifetime.
type regStep struct {
	// Op is "chunk" (a chunk arrives for ID/Total), "poison" (a protocol
	// violation was detected for ID), "complete" (the transfer finished), or
	// "prune" (the periodic staleness sweep runs).
	Op    string
	ID    uint64
	Total uint32
	// AtMs is the logical clock, in milliseconds, at which the op happens.
	AtMs int64
	// Want is the admission verdict, for Op == "chunk" only:
	//   "opened"    — a new transfer was created
	//   "existing"  — an already-open transfer was resumed
	//   "poisoned"  — refused: the id is inside the poison window
	//   "zerototal" — refused: total_size == 0
	//   "toolarge"  — refused: total_size over the per-transfer cap
	//   "toomany"   — refused: at the concurrent-transfer bound after pruning
	Want string
	// Transfers/Poisoned are the two map sizes AFTER the op. Asserted on every
	// step: the reclaim rules are only meaningful as changes in these counts.
	Transfers int
	Poisoned  int
}

type regCase struct {
	Name         string
	MaxTotal     uint32
	MaxTransfers int
	MaxPoisoned  int
	TTLMs        int64
	Steps        []regStep
}

var chunkRegistryCases = []regCase{
	{
		// The ordinary lifecycle, and that a COMPLETED id is immediately
		// reusable (Complete must not poison).
		Name: "OpenResumeCompleteReopen", MaxTotal: 1000, MaxTransfers: 4, MaxPoisoned: 4, TTLMs: 60000,
		Steps: []regStep{
			{Op: "chunk", ID: 1, Total: 100, AtMs: 0, Want: "opened", Transfers: 1},
			{Op: "chunk", ID: 1, Total: 100, AtMs: 1000, Want: "existing", Transfers: 1},
			{Op: "complete", ID: 1, AtMs: 1000},
			{Op: "chunk", ID: 1, Total: 100, AtMs: 2000, Want: "opened", Transfers: 1},
		},
	},
	{
		// total_size == 0 would allocate a zero-length buffer that satisfies the
		// `0 == 0` completion test immediately and hands the consumer a null
		// data pointer. Refused — and, being a RESOURCE refusal, it must NOT
		// poison the id: the very next well-formed chunk opens normally.
		Name: "ZeroTotalRefusedWithoutPoison", MaxTotal: 1000, MaxTransfers: 4, MaxPoisoned: 4, TTLMs: 60000,
		Steps: []regStep{
			{Op: "chunk", ID: 7, Total: 0, AtMs: 0, Want: "zerototal"},
			{Op: "chunk", ID: 7, Total: 10, AtMs: 0, Want: "opened", Transfers: 1},
		},
	},
	{
		// The per-transfer size cap, at the boundary: cap+1 refused, cap
		// accepted. Same "refusal does not poison" rule.
		Name: "TotalCapBoundary", MaxTotal: 1000, MaxTransfers: 4, MaxPoisoned: 4, TTLMs: 60000,
		Steps: []regStep{
			{Op: "chunk", ID: 1, Total: 1001, AtMs: 0, Want: "toolarge"},
			{Op: "chunk", ID: 1, Total: 1000, AtMs: 0, Want: "opened", Transfers: 1},
		},
	},
	{
		// PRUNE-THEN-REFUSE at the transfer bound. With two live transfers and a
		// bound of two, a third is refused; once the first two have gone stale,
		// the same request reclaims them and succeeds. No LRU eviction — a live
		// transfer is never dropped to admit a new one.
		Name: "TransferBoundPrunesThenRefuses", MaxTotal: 1000, MaxTransfers: 2, MaxPoisoned: 4, TTLMs: 10000,
		Steps: []regStep{
			{Op: "chunk", ID: 1, Total: 10, AtMs: 0, Want: "opened", Transfers: 1},
			{Op: "chunk", ID: 2, Total: 10, AtMs: 1000, Want: "opened", Transfers: 2},
			{Op: "chunk", ID: 3, Total: 10, AtMs: 2000, Want: "toomany", Transfers: 2},
			// A resumed transfer refreshes lastUpdate, so id 2 survives the
			// reclaim below while id 1 (untouched since 0) does not.
			{Op: "chunk", ID: 2, Total: 10, AtMs: 11000, Want: "existing", Transfers: 2},
			{Op: "chunk", ID: 3, Total: 10, AtMs: 11500, Want: "opened", Transfers: 2},
		},
	},
	{
		// A poisoned id is refused for the whole window, INCLUDING exactly at
		// the TTL, and becomes reusable one tick later — at which point the
		// expired record is dropped, not merely ignored.
		Name: "PoisonRefusesUntilTtlThenExpires", MaxTotal: 1000, MaxTransfers: 4, MaxPoisoned: 4, TTLMs: 10000,
		Steps: []regStep{
			{Op: "chunk", ID: 1, Total: 10, AtMs: 0, Want: "opened", Transfers: 1},
			// Poisoning drops the live buffer as well as recording the id.
			{Op: "poison", ID: 1, AtMs: 1000, Poisoned: 1},
			{Op: "chunk", ID: 1, Total: 10, AtMs: 2000, Want: "poisoned", Poisoned: 1},
			{Op: "chunk", ID: 1, Total: 10, AtMs: 11000, Want: "poisoned", Poisoned: 1},
			{Op: "chunk", ID: 1, Total: 10, AtMs: 11001, Want: "opened", Transfers: 1},
		},
	},
	{
		// At the poison-set bound with nothing expired, the OLDEST record is
		// evicted. Losing a record only restores the resurrect-until-TTL
		// behavior for that one id, which is why eviction is acceptable here and
		// not at the transfer bound.
		Name: "PoisonSetEvictsOldest", MaxTotal: 1000, MaxTransfers: 4, MaxPoisoned: 3, TTLMs: 1000000,
		Steps: []regStep{
			{Op: "poison", ID: 1, AtMs: 1000, Poisoned: 1},
			{Op: "poison", ID: 2, AtMs: 2000, Poisoned: 2},
			{Op: "poison", ID: 3, AtMs: 3000, Poisoned: 3},
			{Op: "poison", ID: 4, AtMs: 4000, Poisoned: 3},
			// id 1 was the oldest, so it is the one that lost its record.
			{Op: "chunk", ID: 1, Total: 10, AtMs: 5000, Want: "opened", Transfers: 1, Poisoned: 3},
			{Op: "chunk", ID: 2, Total: 10, AtMs: 5000, Want: "poisoned", Transfers: 1, Poisoned: 3},
			{Op: "chunk", ID: 4, Total: 10, AtMs: 5000, Want: "poisoned", Transfers: 1, Poisoned: 3},
		},
	},
	{
		// The same eviction, with the id order REVERSED against the time order.
		// Without this case a "drop the map's first entry" implementation looks
		// correct: std::map is keyed by id, so the lowest id and the oldest
		// record coincide whenever ids are poisoned in ascending order. Here the
		// oldest record (id 30) is the map's LAST entry.
		Name: "PoisonSetEvictsOldestNotLowestId", MaxTotal: 1000, MaxTransfers: 4, MaxPoisoned: 3, TTLMs: 1000000,
		Steps: []regStep{
			{Op: "poison", ID: 30, AtMs: 1000, Poisoned: 1},
			{Op: "poison", ID: 20, AtMs: 2000, Poisoned: 2},
			{Op: "poison", ID: 10, AtMs: 3000, Poisoned: 3},
			{Op: "poison", ID: 40, AtMs: 4000, Poisoned: 3},
			{Op: "chunk", ID: 30, Total: 10, AtMs: 5000, Want: "opened", Transfers: 1, Poisoned: 3},
			{Op: "chunk", ID: 10, Total: 10, AtMs: 5000, Want: "poisoned", Transfers: 1, Poisoned: 3},
			{Op: "chunk", ID: 20, Total: 10, AtMs: 5000, Want: "poisoned", Transfers: 1, Poisoned: 3},
			{Op: "chunk", ID: 40, Total: 10, AtMs: 5000, Want: "poisoned", Transfers: 1, Poisoned: 3},
		},
	},
	{
		// At the same bound, expiry is tried FIRST: if pruning frees space, no
		// live record is evicted.
		Name: "PoisonSetPrunesBeforeEvicting", MaxTotal: 1000, MaxTransfers: 4, MaxPoisoned: 3, TTLMs: 10000,
		Steps: []regStep{
			{Op: "poison", ID: 1, AtMs: 0, Poisoned: 1},
			{Op: "poison", ID: 2, AtMs: 1000, Poisoned: 2},
			{Op: "poison", ID: 3, AtMs: 2000, Poisoned: 3},
			{Op: "poison", ID: 4, AtMs: 20000, Poisoned: 1},
			{Op: "chunk", ID: 1, Total: 10, AtMs: 20000, Want: "opened", Transfers: 1, Poisoned: 1},
			{Op: "chunk", ID: 4, Total: 10, AtMs: 20000, Want: "poisoned", Transfers: 1, Poisoned: 1},
		},
	},
	{
		// The periodic sweep expires BOTH maps on the same clock — a poison
		// record is never permanently burned, and an abandoned transfer never
		// squats a slot forever.
		Name: "SweepExpiresBothMaps", MaxTotal: 1000, MaxTransfers: 4, MaxPoisoned: 4, TTLMs: 10000,
		Steps: []regStep{
			{Op: "chunk", ID: 1, Total: 10, AtMs: 0, Want: "opened", Transfers: 1},
			{Op: "poison", ID: 2, AtMs: 0, Transfers: 1, Poisoned: 1},
			{Op: "prune", AtMs: 5000, Transfers: 1, Poisoned: 1},
			{Op: "prune", AtMs: 10000, Transfers: 1, Poisoned: 1},
			{Op: "prune", AtMs: 10001, Transfers: 0, Poisoned: 0},
		},
	},
}

// ---------------------------------------------------------------------------
// Go side of the shared bookkeeping table
// ---------------------------------------------------------------------------

// goAdmit replays one "chunk" step through the GO reassembler in the exact order
// pkg/server/handlers.go::HandleChunk applies it: the poison check first, then
// GetChunkBuffer's admission rules. The refusal REASON is read from the
// sentinel errors rather than the message text, so this cannot silently start
// accepting a differently-refused chunk.
//
// opened-vs-existing is decided by BUFFER IDENTITY (`seen` remembers the last
// *ChunkBuffer handed out per id), not by watching the transfer count: at the
// concurrent-transfer bound GetChunkBuffer prunes and re-inserts in the same
// call, so the count can be unchanged across a genuine open. A different pointer
// means a new buffer was allocated, which is exactly what the C++ mirror's
// ChunkAdmission::Opened means.
func goAdmit(cm *server.ChunkManager, seen map[uint64]*server.ChunkBuffer, s regStep) string {
	if cm.IsPoisoned(s.ID) {
		return "poisoned"
	}
	buf, err := cm.GetChunkBuffer(s.ID, int(s.Total))
	switch {
	case errors.Is(err, server.ErrChunkTotalNonPositive):
		return "zerototal"
	case errors.Is(err, server.ErrChunkTotalTooLarge):
		return "toolarge"
	case errors.Is(err, server.ErrTooManyChunkTransfers):
		return "toomany"
	case err != nil:
		return "err:" + err.Error()
	case seen[s.ID] == buf:
		return "existing"
	default:
		seen[s.ID] = buf
		return "opened"
	}
}

// TestChunkRegistryCasesMatchGoChunkManager is half of the bookkeeping mirror:
// it proves the shared table describes the GO manager's real behavior. The
// native gate below holds xll::ChunkRegistry to the same table.
func TestChunkRegistryCasesMatchGoChunkManager(t *testing.T) {
	for _, c := range chunkRegistryCases {
		t.Run(c.Name, func(t *testing.T) {
			var atMs atomic.Int64
			base := time.Unix(0, 0)
			cm := server.NewChunkManagerFromConfig(server.ChunkManagerConfig{
				MaxBufferBytes:         int64(c.MaxTotal),
				MaxConcurrentTransfers: c.MaxTransfers,
				MaxPoisonedTransfers:   c.MaxPoisoned,
				BufferTTL:              time.Duration(c.TTLMs) * time.Millisecond,
				// Far beyond any test's lifetime: the sweep must happen only
				// where the table says "prune".
				CleanupInterval: time.Hour,
				Clock:           func() time.Time { return base.Add(time.Duration(atMs.Load()) * time.Millisecond) },
			})
			defer cm.Close()

			seen := make(map[uint64]*server.ChunkBuffer)
			for i, s := range c.Steps {
				atMs.Store(s.AtMs)
				switch s.Op {
				case "chunk":
					got := goAdmit(cm, seen, s)
					if got != s.Want {
						t.Fatalf("step %d (%s id=%d total=%d at=%dms): Go verdict %q, table says %q",
							i, s.Op, s.ID, s.Total, s.AtMs, got, s.Want)
					}
				case "poison":
					cm.PoisonTransfer(s.ID)
				case "complete":
					cm.RemoveChunkBuffer(s.ID)
				case "prune":
					cm.Sweep()
				default:
					t.Fatalf("step %d: unknown op %q", i, s.Op)
				}
				if got := cm.TransferCount(); got != s.Transfers {
					t.Fatalf("step %d (%s id=%d at=%dms): %d live transfers, table says %d",
						i, s.Op, s.ID, s.AtMs, got, s.Transfers)
				}
				if got := cm.PoisonedCount(); got != s.Poisoned {
					t.Fatalf("step %d (%s id=%d at=%dms): %d poison records, table says %d",
						i, s.Op, s.ID, s.AtMs, got, s.Poisoned)
				}
			}
		})
	}
}

// TestChunkReceiverCapsMatchGoConstants pins the four numbers the two
// reassemblers are hand-kept equal on, by reading them out of the SHIPPED C++
// header rather than trusting a comment.
//
// Read the direction carefully (AGENTS.md §18.6.1) — these are not one constant
// each:
//   - kMaxChunkTotalSize is the GUEST->HOST receiver's fixed cap; its real twin is
//     the Go SENDER's chunk.MaxTransferBytes, which must equal it because a
//     sender cannot discover it at runtime.
//   - server.DefaultMaxChunkBufferBytes is the DEFAULT of the opposite
//     direction's per-project knob, equal by hand only.
func TestChunkReceiverCapsMatchGoConstants(t *testing.T) {
	m, err := Assets()
	if err != nil {
		t.Fatalf("Assets(): %v", err)
	}
	hdr, ok := m["include/xll_worker.h"]
	if !ok {
		t.Fatalf("embedded include/xll_worker.h not found in assets")
	}

	for _, tc := range []struct {
		marker string
		why    string
	}{
		{"kMaxChunkTotalSize = 256ull * 1024 * 1024",
			"the guest->host receiver cap; pkg/chunk.MaxTransferBytes refuses over-cap payloads against exactly this number"},
		{"kMaxPartialMessages = 1024",
			"mirrors server.DefaultMaxConcurrentTransfers"},
		{"kMaxPoisonedTransfers = 1024",
			"mirrors server.DefaultMaxPoisonedTransfers"},
		{"kChunkStaleTtl{60}",
			"mirrors server.DefaultChunkBufferTTL"},
	} {
		if !strings.Contains(hdr, tc.marker) {
			t.Errorf("xll_worker.h no longer declares %q (%s). If the C++ cap moved on purpose, move its Go "+
				"counterpart in the SAME change — a sender that guesses the receiver's cap wrong "+
				"either drops payloads the host would accept or pushes payloads it will refuse", tc.marker, tc.why)
		}
	}

	if chunk.MaxTransferBytes != 256*1024*1024 {
		t.Errorf("chunk.MaxTransferBytes = %d but the C++ receiver caps at 256 MiB", chunk.MaxTransferBytes)
	}
	if server.DefaultMaxConcurrentTransfers != 1024 || server.DefaultMaxPoisonedTransfers != 1024 {
		t.Errorf("Go count bounds (%d/%d) drifted from the C++ 1024/1024",
			server.DefaultMaxConcurrentTransfers, server.DefaultMaxPoisonedTransfers)
	}
	if server.DefaultChunkBufferTTL != 60*time.Second {
		t.Errorf("server.DefaultChunkBufferTTL = %v, C++ kChunkStaleTtl is 60s", server.DefaultChunkBufferTTL)
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

// TestChunkRegistryLogicIsExtracted is the same guard for the bookkeeping layer:
// the admission caps, the prune-then-refuse reclaim and the poison set must
// stay in the pure xll::ChunkRegistry, because re-inlining them into
// xll_worker.cpp would leave TestChunkRegistryNativeBehavior passing against a
// header nothing ships through.
func TestChunkRegistryLogicIsExtracted(t *testing.T) {
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
		"class ChunkRegistry",
		"enum class ChunkAdmission",
		"struct PartialMessage",
		"ChunkAdmission Admit(",
		"void Poison(uint64_t id, ChunkTime now)",
		"void Prune(ChunkTime now)",
	} {
		if !strings.Contains(hdr, want) {
			t.Errorf("xll_worker.h must keep the pure transfer registry (%q): it is what lets "+
				"internal/assets/testdata/chunk_registry_native_test.cpp exercise the admission caps, "+
				"prune-then-refuse and the poison set without Excel/FlatBuffers/shm", want)
		}
	}

	for _, want := range []string{
		"g_chunkRegistry.Admit(",
		"g_chunkRegistry.Poison(",
		"g_chunkRegistry.Complete(",
		"g_chunkRegistry.Prune(",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("HandleChunk/CleanupStaleChunks no longer route through %s: the offline gate "+
				"would still pass while the shipped reassembler runs untested code", want)
		}
	}

	// The re-inlined shapes, specifically: the two raw maps and the local
	// poison helper the registry replaced.
	for _, banned := range []string{
		"g_partialMessages.find(",
		"g_partialMessages.insert(",
		"g_poisonedTransfers",
		"PoisonTransferLocked",
		"PruneStaleChunksLocked",
	} {
		if strings.Contains(code, banned) {
			t.Errorf("xll_worker.cpp keeps chunk bookkeeping inline again (%s); it must stay in "+
				"xll::ChunkRegistry so it can be unit-tested offline", banned)
		}
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
	runNativeChunkGate(t, "chunk_segments_native_test", map[string]string{
		"chunk_segment_cases.inc": emitCppCases(chunkSegmentCases),
	})
}

// runNativeChunkGate compiles the EMBEDDED xll_worker.h together with
// testdata/<harness>.cpp — plus whatever generated .inc files the caller passes
// — and runs the result, requiring "0 failures" on stdout.
//
// Shared by the segment gate and the registry gate so the two cannot drift in
// how they build the unit under test (same -std, same warnings-as-errors, same
// "compile the ASSET, not a copy" rule). It needs NOTHING but g++: both units
// are pure types over std::map, so there are no types/flatbuffers/phmap headers
// to locate and no FetchContent cache to prime. Still skipped under -short and
// off Windows, matching its siblings (the header is part of a Windows-only asset
// tree, and a gate that behaves differently per platform is worse than one that
// is uniformly skipped).
func runNativeChunkGate(t *testing.T, harnessName string, generated map[string]string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping C++ compile+run gate in short mode")
	}
	if runtime.GOOS != "windows" {
		t.Skip("xll-gen's C++ assets are Windows-only (AGENTS.md §0.1)")
	}

	gxx, err := exec.LookPath("g++")
	if err != nil {
		t.Skipf("g++ not on PATH; skipping %s compile+run gate", harnessName)
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
	// The generated case tables land next to the harness's include path.
	for name, content := range generated {
		if err := os.WriteFile(filepath.Join(incDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	harness := filepath.Join(filepath.Dir(thisFile), "testdata", harnessName+".cpp")
	if _, err := os.Stat(harness); err != nil {
		t.Fatalf("harness %s missing: %v", harness, err)
	}

	exePath := filepath.Join(dir, harnessName+".exe")
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
		t.Fatalf("%s failed to compile: %v\n%s", harnessName, err, out)
	}

	out, err := exec.Command(exePath).CombinedOutput()
	t.Logf("%s output:\n%s", harnessName, out)
	if err != nil {
		t.Fatalf("%s reported failures (or crashed): %v", harnessName, err)
	}
	if !strings.Contains(string(out), "0 failures") {
		t.Fatalf("%s did not report 0 failures", harnessName)
	}
}

// emitCppRegistryCases renders the shared bookkeeping table as the C++
// initializer the registry harness includes. Same rule as emitCppCases: no
// committed copy, so the two sides cannot drift.
func emitCppRegistryCases(cases []regCase) string {
	var b strings.Builder
	b.WriteString("// GENERATED by internal/assets/chunk_cpp_test.go — do not edit.\n")
	b.WriteString("// Source of truth: chunkRegistryCases in that file, which is also replayed\n")
	b.WriteString("// against pkg/server's ChunkManager by TestChunkRegistryCasesMatchGoChunkManager.\n")
	b.WriteString("const std::vector<Case> kCases = {\n")
	for _, c := range cases {
		fmt.Fprintf(&b, "    {%q, %du, %d, %d, %d, {\n", c.Name, c.MaxTotal, c.MaxTransfers, c.MaxPoisoned, c.TTLMs)
		for _, s := range c.Steps {
			fmt.Fprintf(&b, "        {%q, %dull, %du, %d, %q, %d, %d},\n",
				s.Op, s.ID, s.Total, s.AtMs, s.Want, s.Transfers, s.Poisoned)
		}
		b.WriteString("    }},\n")
	}
	b.WriteString("};\n")
	return b.String()
}

// TestChunkRegistryNativeBehavior compiles the EMBEDDED xll_worker.h together
// with testdata/chunk_registry_native_test.cpp and runs it.
//
// Why this gate exists: the segment arbiter got an offline gate on 2026-07-26,
// but the bookkeeping AROUND it — the zero-total refusal, the total-size cap,
// the concurrent-transfer bound with its prune-then-refuse reclaim, and the
// whole poison set (TTL, its own bound, oldest-eviction) — was still only
// compile-checked, because it lived in xll_worker.cpp. Extracting it into
// xll::ChunkRegistry made it reachable from a bare g++, and this replays the
// same table the Go ChunkManager is held to.
func TestChunkRegistryNativeBehavior(t *testing.T) {
	runNativeChunkGate(t, "chunk_registry_native_test", map[string]string{
		"chunk_registry_cases.inc": emitCppRegistryCases(chunkRegistryCases),
	})
}
