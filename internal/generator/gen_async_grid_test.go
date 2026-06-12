package generator

import (
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/assets"
)

// TestAsyncResultDeferredFreeForDLLOwned is the regression for the async
// grid/numgrid heap-corruption bug found during real-Excel verification
// (2026-06-12).
//
// Symptom: =MyAsyncGrid() (an async function returning a [][]any grid that
// spills) crashed Excel on every recalc with STATUS_HEAP_CORRUPTION
// (0xc0000374, faulting module ntdll). Sync grid/numgrid returns spilled fine.
//
// Root cause: ProcessAsyncBatchResponse (src/xll_async.cpp) assumed
// xlAsyncReturn deep-copies the ENTIRE result XLOPER12 synchronously, then
// freed it immediately via xlAutoFree12. That holds for scalars (value copied
// inline) but is FALSE for xltypeMulti: Excel retains the lparray pointer to
// populate the spill range AFTER the calc transaction, so the synchronous
// delete[] of lparray was a use-after-free → heap corruption.
//
// Fix: a result carrying xlbitDLLFree is owned by Excel after the handoff —
// Excel invokes the registered (exported) xlAutoFree12 callback when it is
// done. The asset must NOT free a DLL-owned result itself; it only returns
// borrowed pool nodes (no xlbitDLLFree) via ReleaseXLOPER12. This mirrors the
// sync return path, which always worked.
//
// This pins the embedded src/xll_async.cpp asset so a refactor cannot silently
// reintroduce the synchronous free. Style mirrors TestRtdConnectDrainCapAlignment.
func TestAsyncResultDeferredFreeForDLLOwned(t *testing.T) {
	t.Parallel()
	m, err := assets.Assets()
	if err != nil {
		t.Fatalf("assets.Assets(): %v", err)
	}
	src, ok := m["src/xll_async.cpp"]
	if !ok {
		t.Fatalf("embedded asset src/xll_async.cpp not found")
	}

	// Isolate ProcessAsyncBatchResponse so assertions can't be satisfied by
	// unrelated code.
	const marker = "ProcessAsyncBatchResponse"
	idx := strings.Index(src, marker)
	if idx < 0 {
		t.Fatalf("ProcessAsyncBatchResponse not found in xll_async.cpp")
	}
	body := src[idx:]

	// The DLL-owned ownership decision must be captured BEFORE the handoff so it
	// is not read off an XLOPER12 Excel may already be processing.
	if !strings.Contains(body, "xlbitDLLFree") {
		t.Errorf("ProcessAsyncBatchResponse no longer references xlbitDLLFree (ownership decision lost)")
	}
	if !strings.Contains(body, "xlAsyncReturn") {
		t.Errorf("ProcessAsyncBatchResponse no longer calls xlAsyncReturn")
	}

	// THE BUG: a synchronous xlAutoFree12(pxResult) call after a SUCCESSFUL
	// xlAsyncReturn frees-before-Excel-consumes an xltypeMulti's lparray →
	// heap corruption. One legitimate xlAutoFree12(pxResult) call remains: the
	// xlAsyncReturn FAILURE branch (rc != xlretSuccess — Excel did not take
	// the result, so its deferred callback never fires and the asset must free
	// locally or leak). Assert that EVERY xlAutoFree12(pxResult) occurrence is
	// preceded (within the preceding 800 bytes — the guard and the call are
	// separated by an explanatory comment block) by the failure guard, so an
	// unconditional/success-path free cannot sneak back in.
	for searchFrom := 0; ; {
		rel := strings.Index(body[searchFrom:], "xlAutoFree12(pxResult)")
		if rel < 0 {
			break
		}
		pos := searchFrom + rel
		windowStart := max(0, pos-800)
		if !strings.Contains(body[windowStart:pos], "rc != xlretSuccess") {
			t.Errorf("ProcessAsyncBatchResponse calls xlAutoFree12(pxResult) without the " +
				"`rc != xlretSuccess` failure guard — a success-path synchronous free is the " +
				"heap-corruption bug (Excel retains an xltypeMulti lparray after the handoff " +
				"and frees it via the deferred xlAutoFree12 callback). DLL-owned results must " +
				"be left for Excel unless xlAsyncReturn itself failed.")
		}
		searchFrom = pos + 1
	}

	// The failure branch itself must exist (a failed xlAsyncReturn would
	// otherwise leak the DLL-owned result — Excel never takes ownership).
	if !strings.Contains(body, "rc != xlretSuccess") {
		t.Errorf("ProcessAsyncBatchResponse missing the xlAsyncReturn failure guard " +
			"(rc != xlretSuccess) — a failed handoff leaks the DLL-owned result")
	}

	// The non-DLL-owned (borrowed pool node) branch must still return the node.
	if !strings.Contains(body, "ReleaseXLOPER12(pxResult)") {
		t.Errorf("ProcessAsyncBatchResponse no longer returns borrowed pool nodes via ReleaseXLOPER12")
	}

	// Verify the deferred-free contract is documented at the site (guards against
	// a well-meaning refactor re-adding a synchronous free).
	if !strings.Contains(body, "dllOwned") {
		t.Errorf("ProcessAsyncBatchResponse missing the dllOwned ownership-capture variable; " +
			"the fix captures xlbitDLLFree BEFORE xlAsyncReturn and only frees non-DLL-owned nodes")
	}
}
