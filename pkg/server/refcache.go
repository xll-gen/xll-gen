package server

import (
	"sync"
)

// RefCache manages cached object references (e.g., large ranges or objects).
type RefCache struct {
	cache map[string][]byte
	mu    sync.RWMutex
}

// NewRefCache creates a new RefCache.
func NewRefCache() *RefCache {
	return &RefCache{
		cache: make(map[string][]byte),
	}
}

// Set stores a value in the cache.
func (c *RefCache) Set(key string, value []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Copy data to ensure independence
	data := make([]byte, len(value))
	copy(data, value)
	c.cache[key] = data
}

// Get retrieves a value from the cache.
func (c *RefCache) Get(key string) ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	val, ok := c.cache[key]
	if !ok {
		return nil, false
	}
	// Return a copy to prevent mutation of cached data
	ret := make([]byte, len(val))
	copy(ret, val)
	return ret, true
}

// Clear removes all entries from the cache.
func (c *RefCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache = make(map[string][]byte)
}
