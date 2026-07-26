package rtd

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"

	"github.com/xll-gen/xll-gen/pkg/log"
)

// updateSender is the minimal surface RunOnce needs to push a result back to a
// single RTD topic. *RtdManager satisfies it; tests inject a fake.
//
// SendUpdate pushes a COMPLETED value (memoizable); SendErrorUpdate pushes an
// ERROR value (is_error=true → the C++ consumer caches it as TRANSIENT: the cell
// paints the error, but memoize/memoize_ttl retention is bypassed so the next
// recalc re-runs the handler). See AGENTS.md §19.3.
type updateSender interface {
	SendUpdate(topicID int32, value any) error
	SendErrorUpdate(topicID int32, value any) error
}

// Compile-time assertion that *RtdManager implements updateSender.
var _ updateSender = (*RtdManager)(nil)

// RunOnce orchestrates the mode:"rtd-once" lifecycle on the server side: it
// runs a normal (sync-shaped) handler EXACTLY once for a freshly-connected RTD
// topic and pushes the single result back via mgr.SendUpdate(topicID, ...).
//
// The handler `fn` has the same shape as a sync handler — it takes a context
// and returns (value, error). On success the value is pushed as-is via
// SendUpdate (the RtdManager maps it onto the protocol.Any union, identical to
// the sync/async `any`-return path). On error, the error STRING is pushed via
// SendErrorUpdate (RtdUpdate.is_error=true) so the cell shows something
// actionable instead of staying stuck at #GETTING_DATA — and the C++ consumer
// caches it as TRANSIENT, so the error is visible for one calc cycle but is
// never frozen by memoize/memoize_ttl as the completed result (the bug this
// routing fixes; see AGENTS.md §19.3).
//
// RunOnce is deliberately synchronous: callers (HandleRtdConnect's onConnect
// dispatch) already run it in a panic-recovered goroutine, so RunOnce does not
// spawn its own. It returns the SendUpdate error (if any) so the caller can
// log it; a nil handler is a programming error and returns immediately.
//
// Context cancellation: if ctx is already cancelled (or is cancelled by the
// handler observing it and returning ctx.Err()), the cancellation is treated
// as a handler error and pushed as its string — the cell will not hang. The
// handler is responsible for honoring ctx; RunOnce does not preempt it.
func RunOnce(ctx context.Context, mgr updateSender, topicID int32, fn func(context.Context) (any, error)) error {
	if fn == nil {
		return fmt.Errorf("rtd.RunOnce: nil handler for topic %d", topicID)
	}
	if mgr == nil {
		return fmt.Errorf("rtd.RunOnce: nil manager for topic %d", topicID)
	}

	// Fast-path: a context already done before the handler runs. Push the
	// cancellation reason rather than executing work that cannot be delivered
	// meaningfully.
	if err := ctx.Err(); err != nil {
		log.Warn("rtd.RunOnce: context already done before handler", "topicID", topicID, "err", err)
		return mgr.SendErrorUpdate(topicID, err.Error())
	}

	value, err := fn(ctx)
	if err != nil {
		log.Error("rtd.RunOnce: handler returned error", "topicID", topicID, "err", err)
		// Push the error string as the topic value (is_error=true) so the cell
		// stops showing #GETTING_DATA and surfaces the failure, cached only as a
		// TRANSIENT entry so memoize/memoize_ttl cannot freeze it.
		return mgr.SendErrorUpdate(topicID, err.Error())
	}

	return mgr.SendUpdate(topicID, value)
}

// gridSender is the surface RunOnceGrid needs: it ships the one-shot grid
// payload to the host (keyed by onceKey) AND can push a scalar RTD update (the
// readiness token, or an error string). *RtdManager satisfies it; tests inject
// a fake.
type gridSender interface {
	SendOnceGrid(key string, payload []byte) error
	SendUpdate(topicID int32, value any) error
	SendErrorUpdate(topicID int32, value any) error
}

// Compile-time assertion that *RtdManager implements gridSender.
var _ gridSender = (*RtdManager)(nil)

// readyOnceSeq is a process-monotonic counter whose stringified value is pushed
// as the RTD readiness token after a grid is delivered. It mirrors Excel-DNA's
// changing-GUID trick: the value itself is never displayed (the C++ wrapper
// reads the cached grid bytes, not this token), but it MUST change on every
// delivery so Excel always observes a value change and recalculates the cell —
// which re-enters the wrapper, which then pulls the spilled grid out of the
// host-side RtdOnceGridRegistry.
var readyOnceSeq int64

// readyToken returns the next process-monotonic readiness token. Each call
// returns a distinct, ever-increasing string so repeated deliveries never
// collide on the same value (which Excel would treat as "no change" and skip
// the recalc).
func readyToken() string {
	return strconv.FormatInt(atomic.AddInt64(&readyOnceSeq, 1), 10)
}

// RunOnceGrid is the grid/numgrid twin of RunOnce for mode:"rtd-once" functions
// that SPILL. RTD can only deliver a scalar, so the grid cannot ride the RTD
// update itself; instead RunOnceGrid:
//
//  1. runs the handler exactly once and serializes its result into a
//     protocol.RtdOnceGridResult (this is what `run` returns as bytes — the
//     concrete grid-vs-numgrid serialization lives in the generated server
//     where the return type is known, so pkg/rtd stays type-agnostic);
//  2. ships those bytes guest->host via mgr.SendOnceGrid(onceKey, payload),
//     which BLOCKS until the host has ACKed (and therefore stored) the grid;
//  3. ONLY THEN pushes a changing readiness token via mgr.SendUpdate(topicID,
//     readyToken()), which makes Excel recalc the cell and re-enter the C++
//     wrapper.
//
// ORDERING IS LOAD-BEARING: the grid MUST be delivered and ACKed BEFORE the
// readiness signal. The readiness signal triggers a recalc that re-enters the
// wrapper, and the wrapper looks the grid up in the host-side
// RtdOnceGridRegistry by onceKey. If the readiness token were pushed first (or
// concurrently), the recalc could re-enter the wrapper before the grid landed,
// the lookup would miss, the wrapper would re-issue xlfRtd against the
// still-connected topic, and — the one-shot handler having already run — no
// further update would arrive: the cell would stay stuck at #GETTING_DATA. By
// making SendOnceGrid synchronous and pushing the token only after it succeeds,
// the host is guaranteed to have the grid before the recalc can ask for it.
//
// Error handling mirrors RunOnce: on a handler/serialize error or a
// SendOnceGrid failure the error STRING is pushed via SendUpdate so the cell
// surfaces the failure instead of hanging. A SendOnceGrid failure does NOT push
// a fresh readiness token (there is nothing to spill, so triggering a recalc
// would just loop the wrapper back to a miss).
//
// onceKey is the host-side grid lookup key: the RTD topic strings joined with
// U+001F (\x1f), identical to the C++ wrapper's MakeRtdOnceKey(topics). The
// caller (the generated connect dispatch) passes strings.Join(args, "\x1f").
//
// Like RunOnce, RunOnceGrid is synchronous (the caller already runs it in a
// panic-recovered goroutine) and honors ctx only at the fast-path check; the
// handler is responsible for observing ctx thereafter.
func RunOnceGrid(ctx context.Context, mgr gridSender, topicID int32, onceKey string,
	run func(context.Context) (payload []byte, err error)) error {
	if run == nil {
		return fmt.Errorf("rtd.RunOnceGrid: nil handler for topic %d", topicID)
	}
	if mgr == nil {
		return fmt.Errorf("rtd.RunOnceGrid: nil manager for topic %d", topicID)
	}

	// Fast-path: a context already done before the handler runs. Push the
	// cancellation reason rather than executing work that cannot be delivered.
	if err := ctx.Err(); err != nil {
		log.Warn("rtd.RunOnceGrid: context already done before handler", "topicID", topicID, "err", err)
		return mgr.SendErrorUpdate(topicID, err.Error())
	}

	payload, err := run(ctx)
	if err != nil {
		log.Error("rtd.RunOnceGrid: handler returned error", "topicID", topicID, "err", err)
		// Push the error string as the topic value (is_error=true); never ship a
		// grid, and never let the error be retained past this calc cycle.
		// GRID ROUTING (host side, 2026-07-26): ProcessRtdUpdate recognizes the
		// topic as grid-once and stores this message in RtdOnceGridRegistry as a
		// TRANSIENT entry rather than in the scalar registry. The wrapper then
		// takes the HIT path — a `grid` cell paints this text, a `numgrid` cell
		// paints the empty 0x0 FP12 its ABI limits it to (text goes to the log) —
		// and, because a hit means no xlfRtd, the topic disconnects, the transient
		// entry is reclaimed like a plain `once` entry, and the next recalc
		// re-runs the handler. is_error is what makes that reclaim bypass
		// memoize/memoize_ttl. See AGENTS.md §19.3.
		return mgr.SendErrorUpdate(topicID, err.Error())
	}

	// Deliver the grid to the host and wait for the ACK BEFORE signaling
	// readiness (see the ordering note above).
	if err := mgr.SendOnceGrid(onceKey, payload); err != nil {
		log.Error("rtd.RunOnceGrid: SendOnceGrid failed", "topicID", topicID, "onceKey", onceKey, "err", err)
		// The grid never landed; surface the error (is_error=true) and do NOT
		// push a fresh readiness token (a recalc would just miss the absent grid).
		return mgr.SendErrorUpdate(topicID, err.Error())
	}

	// Grid is delivered+acked: now trigger the recalc that re-enters the
	// wrapper and pulls the spilled grid back out.
	return mgr.SendUpdate(topicID, readyToken())
}
