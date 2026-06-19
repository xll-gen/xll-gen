// Package transferid generates the 64-bit correlation IDs that tag chunked
// transfers across the SHM boundary. It is a leaf package (imports only
// pkg/log) so both pkg/server and pkg/rtd can share one generator despite the
// server->rtd import cycle (pkg/server imports pkg/rtd via NewSystemHandler)
// that previously forced each chunk-framing site to roll its own ID.
//
// All IDs come from crypto/rand so the host->guest and guest->host paths share
// a single 64-bit keyspace. The former guest-side path used
// uint64(rand.Int63()) — a 63-bit value drawn from the global-mutex math/rand
// source, which both halved the keyspace and serialized concurrent senders.
// New() uses neither. See AGENTS.md §23.3.
package transferid

import (
	"crypto/rand"
	"encoding/binary"
	mrand "math/rand/v2"

	"github.com/xll-gen/xll-gen/pkg/log"
)

// New returns a fresh 64-bit transfer ID.
func New() uint64 {
	var b [8]byte
	_, err := rand.Read(b[:])
	if err != nil {
		// crypto/rand should essentially never fail, but if it does,
		// returning a constant 0 would collide every concurrent transfer
		// onto the same correlation key — guaranteed corruption. Fall back
		// to math/rand/v2's Uint64 (auto-seeded, lock-free) so IDs stay
		// distinct even on the degraded path.
		log.Error("transferid.New: crypto/rand failed, falling back to math/rand/v2", "err", err)
		return mrand.Uint64()
	}
	return binary.LittleEndian.Uint64(b[:])
}
