#pragma once

#include <flatbuffers/flatbuffers.h>

// Custom Allocator for FlatBuffers to use Shared Memory
class SHMAllocator : public flatbuffers::Allocator {
private:
    uint8_t* buffer_;
    size_t size_;

public:
    SHMAllocator(uint8_t* buffer, size_t size)
        : buffer_(buffer), size_(size) {}

    uint8_t* allocate(size_t size) override {
        if (size > size_) {
            return nullptr;
        }
        return buffer_;
    }

    void deallocate(uint8_t* p, size_t size) override {
        // Shared Memory, do nothing
    }
};
