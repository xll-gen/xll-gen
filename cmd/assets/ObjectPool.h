#ifndef OBJECT_POOL_H
#define OBJECT_POOL_H

#include <vector>
#include <mutex>

template <typename T>
class ObjectPool {
public:
    T* Acquire() {
        std::lock_guard<std::mutex> lock(mutex_);
        if (pool_.empty()) {
            return new T();
        }
        T* item = pool_.back();
        pool_.pop_back();
        return item;
    }

    void Release(T* item) {
        if (!item) return;
        std::lock_guard<std::mutex> lock(mutex_);
        pool_.push_back(item);
    }

    void Clear() {
        std::lock_guard<std::mutex> lock(mutex_);
        for (T* item : pool_) {
            delete item;
        }
        pool_.clear();
    }

    ~ObjectPool() {
        Clear();
    }

private:
    std::vector<T*> pool_;
    std::mutex mutex_;
};

#endif // OBJECT_POOL_H
