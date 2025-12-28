package server

import (
	"fmt"
	"sync"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/shm/go"
	"github.com/xll-gen/types/go/protocol"
)

// RtdManager manages RTD topic subscriptions and broadcasts.
type RtdManager struct {
	mu sync.RWMutex
	// map[Key] -> map[TopicID]struct{}
	keyToIDs map[string]map[int32]struct{}
	// map[TopicID] -> Key
	idToKey map[int32]string
	client  *shm.Client
}

// GlobalRtd is the singleton instance of RtdManager.
var GlobalRtd = NewRtdManager()

// NewRtdManager creates a new RtdManager.
func NewRtdManager() *RtdManager {
	return &RtdManager{
		keyToIDs: make(map[string]map[int32]struct{}),
		idToKey:  make(map[int32]string),
	}
}

// SetClient sets the SHM client used to send updates.
func (m *RtdManager) SetClient(c *shm.Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.client = c
}

// Subscribe registers a TopicID to a logical key.
// Future calls to Publish(key, value) will update this TopicID.
func (m *RtdManager) Subscribe(key string, topicID int32) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// If this topicID is already subscribed to a different key, unsubscribe first
	if oldKey, ok := m.idToKey[topicID]; ok {
		if oldKey == key {
			return // Already subscribed to this key
		}
		// Remove from old key's set
		delete(m.keyToIDs[oldKey], topicID)
		if len(m.keyToIDs[oldKey]) == 0 {
			delete(m.keyToIDs, oldKey)
		}
	}

	if _, ok := m.keyToIDs[key]; !ok {
		m.keyToIDs[key] = make(map[int32]struct{})
	}
	m.keyToIDs[key][topicID] = struct{}{}
	m.idToKey[topicID] = key
}

// Unsubscribe removes a TopicID from management.
func (m *RtdManager) Unsubscribe(topicID int32) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if key, ok := m.idToKey[topicID]; ok {
		delete(m.keyToIDs[key], topicID)
		if len(m.keyToIDs[key]) == 0 {
			delete(m.keyToIDs, key)
		}
		delete(m.idToKey, topicID)
	}
}

// Publish broadcasts a value to all TopicIDs subscribed to the given key.
func (m *RtdManager) Publish(key string, value interface{}) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ids, ok := m.keyToIDs[key]
	if !ok || len(ids) == 0 {
		return nil
	}

	if m.client == nil {
		return fmt.Errorf("RTD server not connected")
	}

	// Iterate and send updates
	for id := range ids {
		if err := m.sendUpdateLocked(id, value); err != nil {
			return err
		}
	}

	return nil
}

// SendUpdate sends a direct update to a specific TopicID.
func (m *RtdManager) SendUpdate(topicID int32, value interface{}) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sendUpdateLocked(topicID, value)
}

// Manually defining helpers for RtdUpdate since they are missing from types repo.
// The github.com/xll-gen/types v0.1.1 package does not contain RtdUpdate tables yet,
// but the user's generated code (from internal/templates/protocol.fbs) does.
// Since pkg/server is a library that depends on the upstream types repo, we must
// manually implement these helpers until the types repo is updated.
func rtdUpdateStart(builder *flatbuffers.Builder) {
	builder.StartObject(2)
}
func rtdUpdateAddTopicId(builder *flatbuffers.Builder, topicId int32) {
	builder.PrependInt32Slot(0, topicId, 0)
}
func rtdUpdateAddVal(builder *flatbuffers.Builder, val flatbuffers.UOffsetT) {
	builder.PrependUOffsetTSlot(1, val, 0)
}
func rtdUpdateEnd(builder *flatbuffers.Builder) flatbuffers.UOffsetT {
	return builder.EndObject()
}

func (m *RtdManager) sendUpdateLocked(topicID int32, value interface{}) error {
	if m.client == nil {
		return fmt.Errorf("server not connected")
	}

	b := GetBuilder(nil)
	defer PutBuilder(b)

	// Encode Value
	var anyOff flatbuffers.UOffsetT
	var anyType protocol.AnyValue

	switch v := value.(type) {
	case string:
		sOff := b.CreateString(v)
		protocol.StrStart(b)
		protocol.StrAddVal(b, sOff)
		valOff := protocol.StrEnd(b)
		protocol.AnyStart(b)
		protocol.AnyAddValType(b, protocol.AnyValueStr)
		protocol.AnyAddVal(b, valOff)
		anyOff = protocol.AnyEnd(b)
		anyType = protocol.AnyValueStr
	case int:
		protocol.IntStart(b)
		protocol.IntAddVal(b, int32(v))
		valOff := protocol.IntEnd(b)
		protocol.AnyStart(b)
		protocol.AnyAddValType(b, protocol.AnyValueInt)
		protocol.AnyAddVal(b, valOff)
		anyOff = protocol.AnyEnd(b)
		anyType = protocol.AnyValueInt
	case int32:
		protocol.IntStart(b)
		protocol.IntAddVal(b, v)
		valOff := protocol.IntEnd(b)
		protocol.AnyStart(b)
		protocol.AnyAddValType(b, protocol.AnyValueInt)
		protocol.AnyAddVal(b, valOff)
		anyOff = protocol.AnyEnd(b)
		anyType = protocol.AnyValueInt
	case int64:
		protocol.IntStart(b)
		protocol.IntAddVal(b, int32(v))
		valOff := protocol.IntEnd(b)
		protocol.AnyStart(b)
		protocol.AnyAddValType(b, protocol.AnyValueInt)
		protocol.AnyAddVal(b, valOff)
		anyOff = protocol.AnyEnd(b)
		anyType = protocol.AnyValueInt
	case float64:
		protocol.NumStart(b)
		protocol.NumAddVal(b, v)
		valOff := protocol.NumEnd(b)
		protocol.AnyStart(b)
		protocol.AnyAddValType(b, protocol.AnyValueNum)
		protocol.AnyAddVal(b, valOff)
		anyOff = protocol.AnyEnd(b)
		anyType = protocol.AnyValueNum
	case float32:
		protocol.NumStart(b)
		protocol.NumAddVal(b, float64(v))
		valOff := protocol.NumEnd(b)
		protocol.AnyStart(b)
		protocol.AnyAddValType(b, protocol.AnyValueNum)
		protocol.AnyAddVal(b, valOff)
		anyOff = protocol.AnyEnd(b)
		anyType = protocol.AnyValueNum
	case bool:
		protocol.BoolStart(b)
		protocol.BoolAddVal(b, v)
		valOff := protocol.BoolEnd(b)
		protocol.AnyStart(b)
		protocol.AnyAddValType(b, protocol.AnyValueBool)
		protocol.AnyAddVal(b, valOff)
		anyOff = protocol.AnyEnd(b)
		anyType = protocol.AnyValueBool
	case time.Time:
		sOff := b.CreateString(v.Format(time.RFC3339))
		protocol.StrStart(b)
		protocol.StrAddVal(b, sOff)
		valOff := protocol.StrEnd(b)
		protocol.AnyStart(b)
		protocol.AnyAddValType(b, protocol.AnyValueStr)
		protocol.AnyAddVal(b, valOff)
		anyOff = protocol.AnyEnd(b)
		anyType = protocol.AnyValueStr
	default:
		sOff := b.CreateString(fmt.Sprintf("%v", v))
		protocol.StrStart(b)
		protocol.StrAddVal(b, sOff)
		valOff := protocol.StrEnd(b)
		protocol.AnyStart(b)
		protocol.AnyAddValType(b, protocol.AnyValueStr)
		protocol.AnyAddVal(b, valOff)
		anyOff = protocol.AnyEnd(b)
		anyType = protocol.AnyValueStr
	}
	_ = anyType

	rtdUpdateStart(b)
	rtdUpdateAddTopicId(b, topicID)
	rtdUpdateAddVal(b, anyOff)
	root := rtdUpdateEnd(b)
	b.Finish(root)

	data := b.FinishedBytes()

	// MSG_RTD_UPDATE = 12
	// Use SendGuestCallWithTimeout (1000ms)
	_, err := m.client.SendGuestCallWithTimeout(data, 12, 1000*time.Millisecond)
	return err
}
