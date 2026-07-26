package server

import "testing"

func TestRefCache_SetGetClear(t *testing.T) {
	c := NewRefCache()
	c.Set("k", []byte("hello"))

	got, ok := c.Get("k")
	if !ok || string(got) != "hello" {
		t.Fatalf("Get after Set = %q, %v; want \"hello\", true", got, ok)
	}

	// Get returns a copy: mutating it must not corrupt the cache.
	got[0] = 'X'
	again, _ := c.Get("k")
	if string(again) != "hello" {
		t.Errorf("cache mutated through returned slice: %q", again)
	}

	c.Clear()
	if _, ok := c.Get("k"); ok {
		t.Errorf("key present after Clear")
	}
}

// TestHandleCalculationCanceled_LeavesRefCacheToTheEndedPath replaces the
// former TestHandleCalculationCanceled_ClearsRefCache, which asserted the
// OPPOSITE. That test encoded a premise measurement later disproved (AGENTS.md
// §19.4): it assumed a run of cancellations with no intervening calc-ended
// could leak RefCache entries. Real Excel fires CalculationCanceled and then
// CalculationEnded 2-6 ms later for the SAME interrupted cycle, 3/3 — there is
// no such run, and the Ended path clears the cache either way.
//
// Clearing on cancel is not merely redundant, it is a correctness bug: the C++
// g_sentRefCache and this RefCache stay consistent only because ONE event
// clears both. Dropping the Go side early leaves C++ believing the payload was
// already shipped, so it is never re-sent and ResolveRangeArg misses.
//
// FAIL-before: restoring `h.RefCache.Clear()` in HandleCalculationCanceled
// fails the survives-the-cancel assertion.
func TestHandleCalculationCanceled_LeavesRefCacheToTheEndedPath(t *testing.T) {
	h := &SystemHandler{
		CommandBatcher: NewCommandBatcher(),
		RefCache:       NewRefCache(),
		ChunkManager:   NewChunkManager(),
	}
	h.RefCache.Set("ref1", []byte("payload"))
	if _, ok := h.RefCache.Get("ref1"); !ok {
		t.Fatal("precondition: ref1 should be present")
	}

	// onCanceled = nil: the handler must be a pure notification.
	h.HandleCalculationCanceled(nil)

	if _, ok := h.RefCache.Get("ref1"); !ok {
		t.Fatalf("RefCache lost ref1 on cancel; the clear belongs to the " +
			"CalculationEnded path so the C++ g_sentRefCache stays in lockstep")
	}

	// The CalculationEnded that Excel fires a few ms later is the clear point.
	flushEnded(t, h, nil)

	if _, ok := h.RefCache.Get("ref1"); ok {
		t.Errorf("RefCache still holds ref1 after HandleCalculationEnded")
	}
}
