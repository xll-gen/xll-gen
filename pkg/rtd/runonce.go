package rtd

import (
	"context"
	"fmt"

	"github.com/xll-gen/xll-gen/pkg/log"
)

// updateSender is the minimal surface RunOnce needs to push a result back to a
// single RTD topic. *RtdManager satisfies it; tests inject a fake.
type updateSender interface {
	SendUpdate(topicID int32, value any) error
}

// Compile-time assertion that *RtdManager implements updateSender.
var _ updateSender = (*RtdManager)(nil)

// RunOnce orchestrates the mode:"rtd-once" lifecycle on the server side: it
// runs a normal (sync-shaped) handler EXACTLY once for a freshly-connected RTD
// topic and pushes the single result back via mgr.SendUpdate(topicID, ...).
//
// The handler `fn` has the same shape as a sync handler — it takes a context
// and returns (value, error). On success the value is pushed as-is (the
// RtdManager maps it onto the protocol.Any union, identical to the sync/async
// `any`-return path). On error, the error STRING is pushed as the value so the
// cell shows something actionable instead of staying stuck at #GETTING_DATA;
// this mirrors how the C++ RTD path surfaces unconvertible values and is the
// best fidelity available given RtdUpdate carries no dedicated error channel.
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
		return mgr.SendUpdate(topicID, err.Error())
	}

	value, err := fn(ctx)
	if err != nil {
		log.Error("rtd.RunOnce: handler returned error", "topicID", topicID, "err", err)
		// Push the error string as the topic value so the cell stops showing
		// #GETTING_DATA and surfaces the failure.
		return mgr.SendUpdate(topicID, err.Error())
	}

	return mgr.SendUpdate(topicID, value)
}
