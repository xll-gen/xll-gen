package rtd

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/xll-gen/shm/go"
	"github.com/xll-gen/types/go/protocol"
	"github.com/xll-gen/xll-gen/internal/fbany"
	"github.com/xll-gen/xll-gen/pkg/log"
	"github.com/xll-gen/xll-gen/pkg/pool"
)

// MsgRtdUpdate is the message ID for RTD updates (must match server/types.go)
const MsgRtdUpdate = 135

// rtdClient is the subset of *shm.Client the RtdManager uses. It is an
// interface so tests can inject slow/failing stubs without a real SHM
// segment.
type rtdClient interface {
	SendGuestCallWithTimeout(data []byte, msgType shm.MsgType, timeout time.Duration) ([]byte, error)
}

// RtdManager manages RTD topic subscriptions and broadcasts.
type RtdManager struct {
	mu sync.RWMutex
	// map[Key] -> map[TopicID]struct{}
	keyToIDs map[string]map[int32]struct{}
	// map[TopicID] -> Key
	idToKey map[int32]string
	client  rtdClient
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
	if c == nil {
		// Avoid storing a typed-nil in the interface field, which would
		// defeat the client == nil guards below.
		m.client = nil
		return
	}
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
//
// The subscription map and client are snapshotted under a short read lock and
// the (potentially slow, 1s-timeout-per-topic) SHM sends happen OUTSIDE the
// lock, so Subscribe/Unsubscribe/SetClient are never blocked by a stalled
// host. A send failure for one topic does not starve the remaining topics:
// every topic is attempted, each failure is logged, and the per-topic errors
// are returned joined via errors.Join (nil when all sends succeed).
func (m *RtdManager) Publish(key string, value interface{}) error {
	m.mu.RLock()
	client := m.client
	ids := m.keyToIDs[key]
	topicIDs := make([]int32, 0, len(ids))
	for id := range ids {
		topicIDs = append(topicIDs, id)
	}
	m.mu.RUnlock()

	if len(topicIDs) == 0 {
		return nil
	}

	if client == nil {
		return fmt.Errorf("RTD server not connected")
	}

	// Iterate and send updates (outside the lock; continue past errors)
	var errs []error
	for _, id := range topicIDs {
		if err := sendUpdate(client, id, value); err != nil {
			log.Error("RTD publish failed for topic", "key", key, "topicID", id, "error", err)
			errs = append(errs, fmt.Errorf("topic %d: %w", id, err))
		}
	}

	return errors.Join(errs...)
}

// SendUpdate sends a direct update to a specific TopicID.
func (m *RtdManager) SendUpdate(topicID int32, value interface{}) error {
	m.mu.RLock()
	client := m.client
	m.mu.RUnlock()
	return sendUpdate(client, topicID, value)
}

// sendUpdate serializes value into an RtdUpdate message and sends it via
// client. It takes the client as a parameter (instead of reading m.client)
// so callers can snapshot the client under the manager lock and perform the
// blocking SHM send after releasing it.
func sendUpdate(client rtdClient, topicID int32, value interface{}) error {
	if client == nil {
		return fmt.Errorf("server not connected")
	}

	b := pool.GetBuilder(nil)
	defer pool.PutBuilder(b)

	// Map the Go value onto a protocol.Any union tag + payload (canonical
	// mapping shared with the generated sync/async `any`-return paths).
	anyOff := fbany.BuildGo(b, value)

	protocol.RtdUpdateStart(b)
	protocol.RtdUpdateAddTopicId(b, topicID)
	protocol.RtdUpdateAddVal(b, anyOff)
	root := protocol.RtdUpdateEnd(b)
	b.Finish(root)

	data := b.FinishedBytes()

	_, err := client.SendGuestCallWithTimeout(data, MsgRtdUpdate, 1000*time.Millisecond)
	return err
}
