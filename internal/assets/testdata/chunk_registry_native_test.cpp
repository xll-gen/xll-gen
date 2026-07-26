// Offline unit test for the guest->host chunk reassembler's TRANSFER
// BOOKKEEPING — the layer around the segment arbiter.
//
// WHAT THIS EXISTS FOR. chunk_segments_native_test.cpp (2026-07-26) closed the
// gap for one arriving RANGE. Everything around it stayed untested: which
// transfers may be OPENED (zero total_size, the per-transfer size cap, the
// concurrent-transfer bound and its prune-then-refuse reclaim) and how a refused
// one is REMEMBERED (the poison set: refuse-until-TTL, expiry, its own bound,
// oldest-eviction). All of that lived in internal/assets/files/src/
// xll_worker.cpp, which cannot be linked outside the XLL — its completion path
// reaches xlAsyncReturn, COM and the shm host — so the cmake gates only ever
// proved it compiles.
//
// The unit under test is xll::ChunkRegistry (internal/assets/files/include/
// xll_worker.h), the pure extraction of that bookkeeping: two std::maps, no
// Excel, no FlatBuffers, no shm, no logging, no clock of its own (every TTL
// decision takes `now` as an argument, which is what lets this file drive expiry
// exactly instead of sleeping).
//
// THE CASE TABLE IS NOT WRITTEN HERE. internal/assets/chunk_cpp_test.go owns one
// table, replays it against Go's ChunkManager (GetChunkBuffer / PoisonTransfer /
// IsPoisoned / the sweep), and emits it into this file as
// chunk_registry_cases.inc. Both sides therefore assert the SAME rules by
// construction rather than by two hand-maintained lists drifting apart.
//
// Build/run: driven by internal/assets/chunk_cpp_test.go
// (TestChunkRegistryNativeBehavior). Exit code 0 and "0 failures" on stdout mean
// pass.

#include "xll_worker.h"

#include <chrono>
#include <cstdint>
#include <cstdio>
#include <cstring>
#include <string>
#include <vector>

namespace {

int g_failures = 0;
int g_checks = 0;

void Check(bool ok, const std::string& what) {
    ++g_checks;
    if (!ok) {
        ++g_failures;
        std::printf("FAIL: %s\n", what.c_str());
    }
}

const char* AdmissionName(xll::ChunkAdmission a) {
    switch (a) {
    case xll::ChunkAdmission::Opened:                  return "opened";
    case xll::ChunkAdmission::Existing:                return "existing";
    case xll::ChunkAdmission::RefusedPoisoned:         return "poisoned";
    case xll::ChunkAdmission::RefusedZeroTotal:        return "zerototal";
    case xll::ChunkAdmission::RefusedTotalTooLarge:    return "toolarge";
    case xll::ChunkAdmission::RefusedTooManyTransfers: return "toomany";
    }
    return "?";
}

// One operation on the registry, and the state it must leave behind.
struct Step {
    const char* op;    // "chunk" | "poison" | "complete" | "prune"
    uint64_t    id;
    uint32_t    total;
    int64_t     atMs;  // logical clock
    const char* want;  // admission verdict, for op == "chunk"
    int         transfers;
    int         poisoned;
};

// One scenario: the registry's caps plus the ordered operations.
struct Case {
    const char*       name;
    uint32_t          maxTotal;
    int               maxTransfers;
    int               maxPoisoned;
    int64_t           ttlMs;
    std::vector<Step> steps;
};

// chunk_registry_cases.inc defines `const std::vector<Case> kCases`. Generated
// by internal/assets/chunk_cpp_test.go from the same Go table it checks
// ChunkManager against.
#include "chunk_registry_cases.inc"

xll::ChunkTime At(int64_t ms) {
    return xll::ChunkTime{} + std::chrono::milliseconds(ms);
}

void RunCase(const Case& c) {
    xll::ChunkRegistry reg(c.maxTotal,
                           static_cast<size_t>(c.maxTransfers),
                           static_cast<size_t>(c.maxPoisoned),
                           std::chrono::milliseconds(c.ttlMs));

    for (size_t i = 0; i < c.steps.size(); ++i) {
        const Step& s = c.steps[i];
        const xll::ChunkTime now = At(s.atMs);

        char loc[256];
        std::snprintf(loc, sizeof(loc), "%s step %zu (%s id=%llu total=%u at=%lldms)",
                      c.name, i, s.op, static_cast<unsigned long long>(s.id), s.total,
                      static_cast<long long>(s.atMs));

        if (std::strcmp(s.op, "chunk") == 0) {
            xll::PartialMessage* pm = nullptr;
            const xll::ChunkAdmission got = reg.Admit(s.id, s.total, /*msgType=*/42, now, &pm);
            Check(std::strcmp(AdmissionName(got), s.want) == 0,
                  std::string(loc) + ": got \"" + AdmissionName(got) + "\", want \"" + s.want + "\"");

            const bool admitted = (got == xll::ChunkAdmission::Opened ||
                                   got == xll::ChunkAdmission::Existing);
            // An admitted chunk MUST come with a usable entry, and a refused one
            // MUST NOT: HandleChunk dereferences the entry immediately after the
            // switch, so a refusal that still published a pointer (or an
            // admission that did not) is a crash or a silently stuck transfer.
            Check(admitted == (pm != nullptr),
                  std::string(loc) + ": entry pointer must be published iff the chunk was admitted");
            if (admitted && pm) {
                Check(pm->buffer.size() == pm->totalSize,
                      std::string(loc) + ": the reassembly buffer must be sized to totalSize");
                Check(pm->lastUpdate == now,
                      std::string(loc) + ": an admitted chunk must refresh lastUpdate (or the TTL sweep reclaims a live transfer)");
            }
            if (got == xll::ChunkAdmission::Opened && pm) {
                Check(pm->receivedSize == 0 && pm->receivedSegments.empty(),
                      std::string(loc) + ": a freshly opened transfer must start empty");
                Check(pm->finalMsgType == 42,
                      std::string(loc) + ": the dispatch message type must be recorded at open time");
            }
        } else if (std::strcmp(s.op, "poison") == 0) {
            reg.Poison(s.id, now);
            Check(reg.IsPoisoned(s.id, now),
                  std::string(loc) + ": the id must be poisoned right after Poison()");
        } else if (std::strcmp(s.op, "complete") == 0) {
            reg.Complete(s.id);
            Check(!reg.IsPoisoned(s.id, now),
                  std::string(loc) + ": completing a transfer must NOT poison its id");
        } else if (std::strcmp(s.op, "prune") == 0) {
            reg.Prune(now);
        } else {
            Check(false, std::string(loc) + ": unknown op");
        }

        Check(static_cast<int>(reg.transferCount()) == s.transfers,
              std::string(loc) + ": " + std::to_string(reg.transferCount()) +
                  " live transfers, table says " + std::to_string(s.transfers));
        Check(static_cast<int>(reg.poisonCount()) == s.poisoned,
              std::string(loc) + ": " + std::to_string(reg.poisonCount()) +
                  " poison records, table says " + std::to_string(s.poisoned));
    }
}

// TestDefaultsAreTheShippedCaps: the table above runs with tiny caps so the
// bounds are cheap to reach. This is the assertion that the DEFAULT-constructed
// registry — the one xll_worker.cpp actually instantiates — carries the numbers
// the Go side is hand-kept equal to.
void TestDefaultsAreTheShippedCaps() {
    xll::ChunkRegistry reg;
    Check(reg.maxTotalSize() == 256ull * 1024 * 1024,
          "DefaultsAreTheShippedCaps: kMaxChunkTotalSize must be 256 MiB (pkg/chunk.MaxTransferBytes refuses against it)");
    Check(reg.maxTransfers() == 1024,
          "DefaultsAreTheShippedCaps: kMaxPartialMessages must be 1024 (server.DefaultMaxConcurrentTransfers)");
    Check(reg.maxPoisoned() == 1024,
          "DefaultsAreTheShippedCaps: kMaxPoisonedTransfers must be 1024 (server.DefaultMaxPoisonedTransfers)");
    Check(xll::kChunkStaleTtl == std::chrono::seconds(60),
          "DefaultsAreTheShippedCaps: kChunkStaleTtl must be 60s (server.DefaultChunkBufferTTL)");
}

// TestExistingEntryKeepsTheFirstTotal pins the C++ side of the INTENTIONAL
// asymmetry documented in AGENTS.md §18.6.1: on transfer-id reuse with a
// different declared total, Go resets the buffer in place and lets the re-open
// proceed, while this side keeps the FIRST total for the life of the entry — so
// the re-open's chunks are measured against the stale total, fail the bounds
// check, and the transfer is discarded and poisoned.
//
// It is asserted HERE rather than in the shared table precisely because it is
// NOT a mirror. Do not "align" it without the cross-repo decision §18.6.1 calls
// for.
void TestExistingEntryKeepsTheFirstTotal() {
    xll::ChunkRegistry reg(1000, 4, 4, std::chrono::seconds(60));
    xll::PartialMessage* pm = nullptr;

    Check(reg.Admit(1, 100, 7, At(0), &pm) == xll::ChunkAdmission::Opened,
          "ExistingEntryKeepsTheFirstTotal: first chunk must open the transfer");
    Check(pm && pm->totalSize == 100, "ExistingEntryKeepsTheFirstTotal: totalSize must be the declared 100");

    pm = nullptr;
    Check(reg.Admit(1, 50, 7, At(1000), &pm) == xll::ChunkAdmission::Existing,
          "ExistingEntryKeepsTheFirstTotal: a re-open is resumed, not re-validated");
    Check(pm && pm->totalSize == 100 && pm->buffer.size() == 100,
          "ExistingEntryKeepsTheFirstTotal: the FIRST total must survive the re-open");
}

// TestPoisonSetStaysBounded is the property the oldest-eviction exists for: no
// sequence of distinct bad ids can grow the poison map past its cap, however
// many arrive inside the TTL.
void TestPoisonSetStaysBounded() {
    xll::ChunkRegistry reg(1000, 4, /*maxPoisoned=*/8, std::chrono::seconds(60));
    for (uint64_t id = 1; id <= 200; ++id) {
        reg.Poison(id, At(static_cast<int64_t>(id)));
        if (reg.poisonCount() > 8) {
            Check(false, "PoisonSetStaysBounded: the poison map grew past its cap at id " + std::to_string(id));
            return;
        }
    }
    Check(reg.poisonCount() == 8, "PoisonSetStaysBounded: the map must sit AT the cap after 200 refusals");
    // The most recent refusals are the ones worth remembering: the eviction
    // policy is oldest-first, so the last 8 ids must all still be recorded.
    for (uint64_t id = 193; id <= 200; ++id) {
        Check(reg.IsPoisoned(id, At(200)),
              "PoisonSetStaysBounded: recent id " + std::to_string(id) + " was evicted before an older one");
    }
}

// TestTransferBoundNeverEvictsALiveTransfer is the counterpart policy: unlike
// the poison set, the transfer map must NEVER drop a live entry to admit a new
// one — that would move the failure onto an innocent producer. At the bound with
// nothing stale, the NEW transfer is the one refused.
void TestTransferBoundNeverEvictsALiveTransfer() {
    xll::ChunkRegistry reg(1000, /*maxTransfers=*/2, 4, std::chrono::seconds(60));
    xll::PartialMessage* pm = nullptr;
    reg.Admit(1, 10, 0, At(0), &pm);
    reg.Admit(2, 10, 0, At(0), &pm);

    for (int i = 0; i < 20; ++i) {
        pm = nullptr;
        Check(reg.Admit(100 + i, 10, 0, At(1000), &pm) == xll::ChunkAdmission::RefusedTooManyTransfers,
              "TransferBoundNeverEvictsALiveTransfer: a new transfer must be refused at the bound");
        Check(pm == nullptr, "TransferBoundNeverEvictsALiveTransfer: a refusal must not publish an entry");
    }
    Check(reg.transferCount() == 2, "TransferBoundNeverEvictsALiveTransfer: the two live transfers must survive");
    pm = nullptr;
    Check(reg.Admit(1, 10, 0, At(1000), &pm) == xll::ChunkAdmission::Existing,
          "TransferBoundNeverEvictsALiveTransfer: the original transfer must still be resumable");
}

// TestSizeCapBoundary: exactly at the cap is accepted, one byte over is refused,
// and the refusal allocates nothing. The cap is the only thing standing between
// a wire-supplied total_size and a multi-GiB resize().
void TestSizeCapBoundary() {
    xll::ChunkRegistry reg(/*maxTotalSize=*/1000, 4, 4, std::chrono::seconds(60));
    xll::PartialMessage* pm = nullptr;

    Check(reg.Admit(1, 1001, 0, At(0), &pm) == xll::ChunkAdmission::RefusedTotalTooLarge,
          "SizeCapBoundary: cap+1 must be refused");
    Check(pm == nullptr && reg.transferCount() == 0,
          "SizeCapBoundary: a refused total must allocate nothing");
    Check(!reg.IsPoisoned(1, At(0)),
          "SizeCapBoundary: a RESOURCE refusal must not poison the id (only protocol violations do)");

    Check(reg.Admit(1, 1000, 0, At(0), &pm) == xll::ChunkAdmission::Opened,
          "SizeCapBoundary: exactly at the cap must be accepted");
    Check(pm && pm->buffer.size() == 1000, "SizeCapBoundary: the buffer must be sized to the declared total");
}

} // namespace

int main() {
    for (size_t i = 0; i < kCases.size(); ++i) {
        RunCase(kCases[i]);
    }
    TestDefaultsAreTheShippedCaps();
    TestExistingEntryKeepsTheFirstTotal();
    TestPoisonSetStaysBounded();
    TestTransferBoundNeverEvictsALiveTransfer();
    TestSizeCapBoundary();

    std::printf("chunk_registry_native_test: %d checks, %d failures\n", g_checks, g_failures);
    return g_failures == 0 ? 0 : 1;
}
