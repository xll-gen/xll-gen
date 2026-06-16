package rtd

import (
	"context"
	"testing"
)

// TestRegisterConnectCancel_UnsubscribeCancels: a cancel registered for a topic
// is invoked when that topic is Unsubscribed, cancelling the connect context.
func TestRegisterConnectCancel_UnsubscribeCancels(t *testing.T) {
	m := NewRtdManager()

	ctx, cancel := context.WithCancel(context.Background())
	dereg := m.RegisterConnectCancel(7, cancel)
	defer dereg()

	select {
	case <-ctx.Done():
		t.Fatal("context cancelled before Unsubscribe")
	default:
	}

	m.Unsubscribe(7)

	select {
	case <-ctx.Done():
		if !isCanceled(ctx) {
			t.Fatalf("ctx.Err() = %v, want context.Canceled", ctx.Err())
		}
	default:
		t.Fatal("Unsubscribe did not cancel the in-flight connect context")
	}

	// The entry was removed by Unsubscribe; a generation-safe deregister of the
	// (now gone) entry is a harmless no-op.
	dereg()
}

// TestRegisterConnectCancel_DeregisterStopsLaterUnsubscribe: a connect that
// completes normally deregisters its cancel; a subsequent Unsubscribe for the
// same topicID must NOT invoke the (stale) cancel.
func TestRegisterConnectCancel_DeregisterStopsLaterUnsubscribe(t *testing.T) {
	m := NewRtdManager()

	ctx, cancel := context.WithCancel(context.Background())
	dereg := m.RegisterConnectCancel(11, cancel)

	// Handler completed normally -> deregister.
	dereg()

	// A later disconnect for the same topic must find no entry and leave the
	// (already-finished, but here still-live for the assertion) context alone.
	m.Unsubscribe(11)

	select {
	case <-ctx.Done():
		t.Fatal("Unsubscribe cancelled a context whose connect had already deregistered")
	default:
	}
}

// TestRegisterConnectCancel_GenerationSafety is the core race-correctness test:
// topicIDs are reused after disconnect, so a slow connect goroutine's deferred
// deregister must NOT remove or cancel a NEWER registration that reused the same
// topicID.
func TestRegisterConnectCancel_GenerationSafety(t *testing.T) {
	m := NewRtdManager()
	const topicID = int32(42)

	// First connect on topicID 42.
	ctxOld, cancelOld := context.WithCancel(context.Background())
	deregOld := m.RegisterConnectCancel(topicID, cancelOld)

	// Excel reuses topicID 42 for a brand-new connect. Registering the new one
	// cancels the stale first registration (it should no longer be in flight on
	// this id) and installs a fresh generation.
	ctxNew, cancelNew := context.WithCancel(context.Background())
	deregNew := m.RegisterConnectCancel(topicID, cancelNew)
	defer deregNew()

	// Registering the replacement cancelled the OLD context.
	select {
	case <-ctxOld.Done():
	default:
		t.Fatal("replacing registration did not cancel the stale old context")
	}

	// The OLD connect goroutine finally finishes and runs its deferred
	// deregister. Because it carries the OLD generation, it must be a no-op:
	// the NEW registration stays installed.
	deregOld()

	// Prove the new registration is still live: Unsubscribe must cancel it.
	if isCanceled(ctxNew) {
		t.Fatal("new context was cancelled by the stale deregister (generation safety broken)")
	}
	m.Unsubscribe(topicID)
	select {
	case <-ctxNew.Done():
		if !isCanceled(ctxNew) {
			t.Fatalf("ctxNew.Err() = %v, want context.Canceled", ctxNew.Err())
		}
	default:
		t.Fatal("Unsubscribe did not cancel the new (current-generation) registration")
	}
}

// TestRegisterConnectCancel_ReplaceCancelsStale: registering a second cancel for
// the same topicID without an intervening deregister cancels the stale one
// (a connect that never completed before the id was reused must not leak).
func TestRegisterConnectCancel_ReplaceCancelsStale(t *testing.T) {
	m := NewRtdManager()

	ctx1, cancel1 := context.WithCancel(context.Background())
	_ = m.RegisterConnectCancel(3, cancel1)

	ctx2, cancel2 := context.WithCancel(context.Background())
	dereg2 := m.RegisterConnectCancel(3, cancel2)
	defer dereg2()

	if !isCanceled(ctx1) {
		t.Fatal("replacing a registration did not cancel the stale cancel func")
	}
	if isCanceled(ctx2) {
		t.Fatal("the replacement registration must not be cancelled by its own install")
	}
}

func isCanceled(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}
