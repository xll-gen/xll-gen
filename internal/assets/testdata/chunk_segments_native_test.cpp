// Offline unit test for the guest->host chunk reassembler's segment arbiter.
//
// WHAT THIS EXISTS FOR. The Go host->guest reassembler has
// TestChunkBuffer_ClaimSegment (pkg/server/manager_test.go) plus wire-path
// coverage; the C++ guest->host mirror in
// internal/assets/files/src/xll_worker.cpp had NO automated test at all — the
// cmake compile gates only proved it compiles, and regtest's mock_host cases
// 16a..17e drive the GO guest, not this code. This harness closes that gap the
// same way internal/assets/testdata/cache_native_test.cpp does for
// xll_cache.cpp: compile the EMBEDDED asset offline and run assertions against
// it.
//
// The unit under test is xll::ClaimChunkSegment (internal/assets/files/include/
// xll_worker.h), the pure extraction of HandleChunk's bounds check +
// zero-length refusal + overlap arbitration. It needs no Excel, no
// FlatBuffers, no shm and no logging, so this file compiles with a bare g++ and
// the single -I pointing at the embedded include dir.
//
// The CASE TABLE IS NOT WRITTEN HERE. internal/assets/chunk_cpp_test.go owns
// one table, replays it against Go's ChunkBuffer.ClaimSegment (plus the two
// caller-side guards HandleChunk applies), and emits it into this file as
// chunk_segment_cases.inc. Both sides therefore assert the SAME accept/reject
// set by construction rather than by two hand-maintained lists drifting apart.
//
// Build/run: driven by internal/assets/chunk_cpp_test.go
// (TestChunkSegmentNativeBehavior). Exit code 0 and "0 failures" on stdout mean
// pass.

#include "xll_worker.h"

#include <cstdint>
#include <cstdio>
#include <cstring>
#include <map>
#include <string>
#include <vector>

namespace {

int g_failures = 0;
int g_checks = 0;

const char* ClaimName(xll::ChunkSegmentClaim c) {
    switch (c) {
    case xll::ChunkSegmentClaim::New:         return "new";
    case xll::ChunkSegmentClaim::Duplicate:   return "dup";
    case xll::ChunkSegmentClaim::Overlap:     return "overlap";
    case xll::ChunkSegmentClaim::ZeroLength:  return "zero";
    case xll::ChunkSegmentClaim::OutOfBounds: return "oob";
    }
    return "?";
}

void Check(bool ok, const std::string& what) {
    ++g_checks;
    if (!ok) {
        ++g_failures;
        std::printf("FAIL: %s\n", what.c_str());
    }
}

// One arriving chunk range and the verdict the Go mirror produces for it.
struct Step {
    uint32_t    offset;
    uint32_t    length;
    const char* want; // "new" | "dup" | "overlap" | "zero" | "oob"
};

// One transfer: a declared total plus the ordered sequence of arriving ranges.
struct Case {
    const char*       name;
    uint32_t          total;
    std::vector<Step> steps;
};

// chunk_segment_cases.inc defines `const std::vector<Case> kCases`. Generated
// by internal/assets/chunk_cpp_test.go from the same Go table it checks
// ClaimSegment against.
#include "chunk_segment_cases.inc"

// RunCase replays one transfer's steps through ClaimChunkSegment and, on top of
// matching each verdict, asserts the two structural invariants the reassembler
// depends on:
//
//   1. a REJECT verdict must leave the segment map untouched (HandleChunk
//      discards the transfer on those paths without unwinding bookkeeping, so a
//      partial mutation would be invisible corruption if the entry survived);
//   2. the recorded segments must stay ascending AND pairwise disjoint, which
//      together with the caller's `receivedSize == totalSize` test is what
//      makes "complete" mean "every byte written exactly once"
//      (shm SPECIFICATION.md §3.3.4, AGENTS.md §18.6.1).
void RunCase(const Case& c) {
    std::map<uint32_t, uint32_t> segs;
    uint64_t received = 0;

    for (size_t i = 0; i < c.steps.size(); ++i) {
        const Step& s = c.steps[i];
        const std::map<uint32_t, uint32_t> before = segs;

        const xll::ChunkSegmentClaim got =
            xll::ClaimChunkSegment(segs, c.total, s.offset, s.length);

        char loc[256];
        std::snprintf(loc, sizeof(loc), "%s step %zu (off=%u len=%u total=%u)",
                      c.name, i, s.offset, s.length, c.total);

        Check(std::strcmp(ClaimName(got), s.want) == 0,
              std::string(loc) + ": got \"" + ClaimName(got) + "\", want \"" + s.want + "\"");

        const bool accepted = (got == xll::ChunkSegmentClaim::New);
        if (accepted) {
            received += s.length;
            Check(segs.size() == before.size() + 1,
                  std::string(loc) + ": an accepted range must be recorded exactly once");
        } else {
            Check(segs == before,
                  std::string(loc) + ": a non-accepted range must leave the segment map unchanged");
        }

        // Invariant 2: ascending and pairwise disjoint.
        uint64_t prevEnd = 0;
        bool first = true;
        for (std::map<uint32_t, uint32_t>::const_iterator it = segs.begin(); it != segs.end(); ++it) {
            if (!first) {
                Check(it->first >= prevEnd,
                      std::string(loc) + ": segments overlap after the call");
            }
            prevEnd = static_cast<uint64_t>(it->first) + it->second;
            first = false;
            Check(prevEnd <= c.total,
                  std::string(loc) + ": a recorded segment runs past totalSize");
        }
    }

    // Sum of the recorded lengths must equal what the caller would have
    // accumulated in receivedSize — the other half of the completion contract.
    uint64_t sum = 0;
    for (std::map<uint32_t, uint32_t>::const_iterator it = segs.begin(); it != segs.end(); ++it) {
        sum += it->second;
    }
    Check(sum == received,
          std::string(c.name) + ": Sum(segment lengths) != the caller's receivedSize");
}

// TestCoverageImpliesCompleteness is the property the whole arbiter exists to
// guarantee, asserted directly rather than through a case table: for a transfer
// built only from ::New verdicts, reaching Sum == totalSize implies the
// segments tile [0, totalSize) with no gap. Before the overlap rejection
// existed, total=100 with (0,60) then (50,40) summed to 100 while [90,100) had
// never been written and the consumer read zero-fill as payload.
void TestCoverageImpliesCompleteness() {
    const uint32_t total = 100;
    // Every 2-chunk split of [0,100) plus a set of malformed ones.
    for (uint32_t split = 0; split <= total; ++split) {
        std::map<uint32_t, uint32_t> segs;
        uint64_t received = 0;
        if (split > 0 &&
            xll::ClaimChunkSegment(segs, total, 0, split) == xll::ChunkSegmentClaim::New) {
            received += split;
        }
        if (split < total &&
            xll::ClaimChunkSegment(segs, total, split, total - split) == xll::ChunkSegmentClaim::New) {
            received += total - split;
        }
        if (received != total) continue;
        // Complete => contiguous from 0 with no gap.
        uint64_t cursor = 0;
        for (std::map<uint32_t, uint32_t>::const_iterator it = segs.begin(); it != segs.end(); ++it) {
            Check(it->first == cursor,
                  "CoverageImpliesCompleteness: gap in a transfer reported complete");
            cursor = static_cast<uint64_t>(it->first) + it->second;
        }
        Check(cursor == total, "CoverageImpliesCompleteness: coverage ends short of totalSize");
    }
}

// TestAdditiveBoundsFormWouldWrap documents WHY the bounds check is written in
// the subtraction form. With totalSize at the uint32 ceiling, an
// offset+length that wraps uint32 would pass an additive check computed in
// 32-bit and let the memcpy write past the buffer. The assertion is that the
// shipped form refuses it.
void TestAdditiveBoundsFormWouldWrap() {
    std::map<uint32_t, uint32_t> segs;
    const uint32_t total  = 0xFFFFFFFFu;
    const uint32_t offset = 0xFFFFFFF0u;
    const uint32_t length = 0x20u;

    // The naive 32-bit additive form wraps to 0x10 and would "fit".
    Check(static_cast<uint32_t>(offset + length) <= total,
          "AdditiveBoundsFormWouldWrap: the premise no longer holds; pick new operands");
    Check(xll::ClaimChunkSegment(segs, total, offset, length) == xll::ChunkSegmentClaim::OutOfBounds,
          "AdditiveBoundsFormWouldWrap: a range whose uint32 sum WRAPS must be refused");
    Check(segs.empty(), "AdditiveBoundsFormWouldWrap: a refused range must not be recorded");
}

// TestDuplicateIsIdempotentAtScale pins that repeated exact retransmits never
// advance coverage: HandleChunk skips the receivedSize advance on ::Duplicate,
// so a producer replaying the final chunk after a dropped ACK must not push
// receivedSize past totalSize (AGENTS.md §23.3).
void TestDuplicateIsIdempotentAtScale() {
    std::map<uint32_t, uint32_t> segs;
    const uint32_t total = 1000;
    uint64_t received = 0;
    for (int i = 0; i < 50; ++i) {
        const xll::ChunkSegmentClaim c = xll::ClaimChunkSegment(segs, total, 0, 400);
        if (c == xll::ChunkSegmentClaim::New) received += 400;
        Check(i == 0 ? c == xll::ChunkSegmentClaim::New : c == xll::ChunkSegmentClaim::Duplicate,
              "DuplicateIsIdempotentAtScale: replay " + std::to_string(i) + " misclassified");
    }
    Check(received == 400, "DuplicateIsIdempotentAtScale: replays advanced receivedSize");
    Check(segs.size() == 1, "DuplicateIsIdempotentAtScale: replays grew the segment map");
}

} // namespace

int main() {
    for (size_t i = 0; i < kCases.size(); ++i) {
        RunCase(kCases[i]);
    }
    TestCoverageImpliesCompleteness();
    TestAdditiveBoundsFormWouldWrap();
    TestDuplicateIsIdempotentAtScale();

    std::printf("chunk_segments_native_test: %d checks, %d failures\n", g_checks, g_failures);
    return g_failures == 0 ? 0 : 1;
}
