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

// TestHandleCalculationCanceled_ClearsRefCache verifies the canceled-calc path
// drops cached refs, symmetric with HandleCalculationEnded. Without this a run
// of cancellations (no intervening calc-ended) leaks RefCache entries.
func TestHandleCalculationCanceled_ClearsRefCache(t *testing.T) {
	h := &SystemHandler{
		CommandBatcher: NewCommandBatcher(),
		RefCache:       NewRefCache(),
	}
	h.RefCache.Set("ref1", []byte("payload"))
	if _, ok := h.RefCache.Get("ref1"); !ok {
		t.Fatal("precondition: ref1 should be present")
	}

	// onCanceled = nil: only the synchronous Clear paths run.
	h.HandleCalculationCanceled(nil)

	if _, ok := h.RefCache.Get("ref1"); ok {
		t.Errorf("RefCache still holds ref1 after HandleCalculationCanceled")
	}
}
