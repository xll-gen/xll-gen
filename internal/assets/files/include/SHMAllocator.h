#pragma once

#include <flatbuffers/flatbuffers.h>

#include <cstddef>
#include <cstdint>
#include <new>

// Custom Allocator for FlatBuffers to use Shared Memory.
//
// The builder writes the request DIRECTLY into the slot's request buffer, so
// the arena is fixed at `slot.GetMaxReqSize()` bytes (512 KiB with the stock
// slot geometry) and a request that does not fit can never be sent.
//
// ---------------------------------------------------------------------------
// WHY allocate() MUST NOT RETURN nullptr  (HIGH, fixed 2026-07-26)
// ---------------------------------------------------------------------------
// flatbuffers::Allocator has NO failure channel. The base class implements
//
//     uint8_t* reallocate_downward(old_p, old_size, new_size, back, front) {
//         uint8_t* new_p = allocate(new_size);
//         memcpy_downward(old_p, old_size, new_p, new_size, back, front);
//         ...
//     }
//
// and memcpy_downward does `memcpy(new_p + new_size - back, ...)`
// unconditionally. Returning nullptr therefore did not fail the build — it
// memcpy'd into a near-null address and took the whole Excel process down with
// an access violation inside the CRT's memcpy.
//
// That is the mechanism behind "a whole-column reference into a grid argument
// kills Excel": =SumGrid($P:$R) coerces to 3.1M cells, protocol::Grid encodes
// ~28 bytes per cell, the arena overflows on the FIRST growth step and the
// process dies. Measured on the stock showcase build (Excel 16.0.20131.20154,
// MinGW/UCRT): 14,400 cells survive, 19,600 cells die — i.e. exactly where
// 512 KiB / ~28 B per cell puts the cliff. The fault signature varies with
// allocator state (an access violation in the CRT memcpy, or a bad_alloc
// raised earlier out of the coerced array), which is why two reproductions of
// the same input reported two different faulting modules.
//
// The fix has two halves:
//   1. Cheap refusal BEFORE the work: xll::ConvertGridArg bounds the reference
//      AREA (see xll_cache.h, kMaxGridArgCells) so a pathological argument
//      never reaches the builder at all.
//   2. This class: an over-capacity request is latched in `overflowed_` and
//      served from the HEAP, so flatbuffers always gets a valid buffer and the
//      caller can refuse the call cleanly. Callers MUST check Overflowed()
//      before Send() — the bytes are not in shared memory and the size does
//      not fit the slot, so sending is never correct.
class SHMAllocator : public flatbuffers::Allocator {
private:
    uint8_t* buffer_;
    size_t size_;
    bool overflowed_ = false;

public:
    // Upper bound for the heap fallback. It exists only so a corrupt/hostile
    // size cannot turn a refusal into a multi-gigabyte allocation; every
    // legitimate over-capacity request is a few MB at most once
    // kMaxGridArgCells has done its job. Beyond this we are out of options and
    // fall back to the pre-2026-07-26 behavior (nullptr), which is no worse
    // than before and, with the area bound in place, unreachable from Excel.
    static constexpr size_t kMaxFallbackBytes = 64u * 1024u * 1024u;

    SHMAllocator(uint8_t* buffer, size_t size)
        : buffer_(buffer), size_(size) {}

    uint8_t* allocate(size_t size) override {
        if (buffer_ != nullptr && size <= size_) {
            return buffer_;
        }
        // Does not fit the slot (or there is no slot at all — GetReqBuffer()
        // answers nullptr / GetMaxReqSize() answers 0 for an invalid slot).
        // Latch it and serve from the heap so flatbuffers keeps a valid
        // buffer; the caller turns this into a clean error.
        overflowed_ = true;
        if (size > kMaxFallbackBytes) {
            return nullptr;
        }
        return static_cast<uint8_t*>(::operator new(size, std::nothrow));
    }

    void deallocate(uint8_t* p, size_t size) override {
        (void)size;
        // The shared-memory buffer is not ours to free; heap fallbacks are.
        if (p != nullptr && p != buffer_) {
            ::operator delete(p);
        }
    }

    // True once any allocation did not fit the shared-memory request buffer.
    // Sticky: the builder may grow several times and only the FIRST failure
    // proves the payload is unsendable.
    bool Overflowed() const { return overflowed_; }
};
