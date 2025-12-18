#ifndef OBJECT_POOL_H
#define OBJECT_POOL_H

#include <vector>
#include <mutex>
#include <array>
#include <thread>
#include <functional>

// A thread-safe object pool with sharded locking to reduce contention.
template <typename T, size_t ShardCount = 16>
class ObjectPool {
private:
    // Align each shard to 64 bytes to prevent false sharing between threads on different cores.
    struct alignas(64) Shard {
        std::vector<T*> pool;
        std::mutex mutex;
    };

    std::array<Shard, ShardCount> shards_;

    // Helper to determine the shard index for the current thread.
    // We use a simple hash of the thread ID.
    size_t GetShardIndex() const {
        size_t h = std::hash<std::thread::id>{}(std::this_thread::get_id());
        return h % ShardCount;
    }

public:
    T* Acquire() {
        size_t idx = GetShardIndex();
        Shard& shard = shards_[idx];

        std::lock_guard<std::mutex> lock(shard.mutex);
        if (shard.pool.empty()) {
            return new T();
        }
        T* item = shard.pool.back();
        shard.pool.pop_back();
        return item;
    }

    void Release(T* item) {
        if (!item) return;

        // We release back to the current thread's shard, not necessarily the one it came from.
        // This is fine as it keeps thread-local caches hot and balances naturally.
        size_t idx = GetShardIndex();
        Shard& shard = shards_[idx];

        std::lock_guard<std::mutex> lock(shard.mutex);
        shard.pool.push_back(item);
    }

    void Clear() {
        // Lock and clear all shards
        for (auto& shard : shards_) {
            std::lock_guard<std::mutex> lock(shard.mutex);
            for (T* item : shard.pool) {
                delete item;
            }
            shard.pool.clear();
        }
    }

    ~ObjectPool() {
        Clear();
    }
};

#endif // OBJECT_POOL_H
