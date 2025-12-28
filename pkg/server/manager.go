package server

import (
	"sync"
	"time"
)

type ChunkManager struct {
	chunkCache     map[uint64]*ChunkBuffer
	chunkMutex     sync.Mutex
	outgoingChunks map[uint64]*OutgoingChunk
	outgoingMutex  sync.Mutex
}

func NewChunkManager() *ChunkManager {
	cm := &ChunkManager{
		chunkCache:     make(map[uint64]*ChunkBuffer),
		outgoingChunks: make(map[uint64]*OutgoingChunk),
	}
	go cm.cleanupLoop()
	return cm
}

func (cm *ChunkManager) cleanupLoop() {
	ticker := time.NewTicker(30 * time.Second)
	for range ticker.C {
		now := time.Now()
		cm.chunkMutex.Lock()
		for id, buf := range cm.chunkCache {
			if now.Sub(buf.LastAccess) > 60*time.Second {
				delete(cm.chunkCache, id)
			}
		}
		cm.chunkMutex.Unlock()

		cm.outgoingMutex.Lock()
		for id, buf := range cm.outgoingChunks {
			if now.Sub(buf.LastAccess) > 60*time.Second {
				delete(cm.outgoingChunks, id)
			}
		}
		cm.outgoingMutex.Unlock()
	}
}

func (cm *ChunkManager) GetChunkBuffer(id uint64, total int) *ChunkBuffer {
	cm.chunkMutex.Lock()
	buf, exists := cm.chunkCache[id]
	if !exists {
		buf = &ChunkBuffer{
			Data:       make([]byte, total),
			TotalSize:  total,
			LastAccess: time.Now(),
		}
		cm.chunkCache[id] = buf
	}
	buf.LastAccess = time.Now()
	cm.chunkMutex.Unlock()
	return buf
}

func (cm *ChunkManager) RemoveChunkBuffer(id uint64) {
	cm.chunkMutex.Lock()
	delete(cm.chunkCache, id)
	cm.chunkMutex.Unlock()
}

func (cm *ChunkManager) AddOutgoingChunk(id uint64, chunk *OutgoingChunk) {
	cm.outgoingMutex.Lock()
	cm.outgoingChunks[id] = chunk
	cm.outgoingMutex.Unlock()
}

func (cm *ChunkManager) GetNextChunk(id uint64, maxSize int) (chunk []byte, msgType uint32, totalSize int, offset int, found bool) {
	cm.outgoingMutex.Lock()
	defer cm.outgoingMutex.Unlock()

	out, exists := cm.outgoingChunks[id]
	if !exists {
		return nil, 0, 0, 0, false
	}
	out.LastAccess = time.Now()

	currentOffset := out.Offset
	remaining := len(out.Data) - out.Offset
	currentSize := maxSize
	if remaining < maxSize {
		currentSize = remaining
	}

	if currentSize <= 0 {
		delete(cm.outgoingChunks, id)
		return nil, 0, 0, 0, false
	}

	chunk = out.Data[currentOffset : currentOffset+currentSize]
	msgType = out.MsgType
	totalSize = len(out.Data)
	offset = currentOffset

	out.Offset += currentSize
	if out.Offset >= len(out.Data) {
		delete(cm.outgoingChunks, id)
	}

	return chunk, msgType, totalSize, offset, true
}

func (cm *ChunkManager) RemoveOutgoingChunk(id uint64) {
	cm.outgoingMutex.Lock()
	delete(cm.outgoingChunks, id)
	cm.outgoingMutex.Unlock()
}
