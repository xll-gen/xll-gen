package generator

import (
	"strings"
	"testing"

	"github.com/xll-gen/xll-gen/internal/assets"
)

// TestRtdConnectDrainCapAlignment is the regression for IMPROVEMENT_BACKLOG.md
// "[MED-후속] graceful drain timeout cap (2s) < RTD Connect lambda's slot.Send
// SHM timeout (5s)" (AGENTS.md §23.0).
//
// The pre-fix RtdServer::ConnectData detached thread issued a SINGLE
// slot.Send(..., MSG_RTD_CONNECT, 5000) — a single Send that could block in SHM
// for up to 5000 ms. WaitForRtdConnectDrain's cap (xll_lifecycle.cpp) is
// 2000 ms. A Connect that blocked >2 s outlived the drain, so OnAutoClose
// proceeded to `delete g_phost` while the Send was still touching the slot — a
// narrow use-after-free.
//
// The fix mirrors ribbon_addin.cpp::SendCommandInvoke (same structural problem):
// a bounded retry loop of SHORT per-attempt timeouts that re-checks
// g_isUnloading between attempts and re-acquires a FRESH ZeroCopySlot each
// attempt. With a <=250 ms per-attempt timeout the thread observes g_isUnloading
// and returns within ~250 ms of it being set, so the 2000 ms drain cap is
// sufficient with margin — no UAF window.
//
// This test pins the embedded src/xll_rtd.cpp asset (the file that ships inside
// the XLL) so a refactor cannot silently drop the retry loop / unload re-check
// and reintroduce the drain-cap gap. Style mirrors TestRibbonAddinFirstClickRetry.
func TestRtdConnectDrainCapAlignment(t *testing.T) {
	t.Parallel()
	m, err := assets.Assets()
	if err != nil {
		t.Fatalf("assets.Assets(): %v", err)
	}
	src, ok := m["src/xll_rtd.cpp"]
	if !ok {
		t.Fatalf("embedded asset src/xll_rtd.cpp not found")
	}

	// Isolate the ConnectData function body so the assertions cannot be
	// satisfied by unrelated code elsewhere in the file (DisconnectData also
	// sends, and ProcessRtdUpdate is above it).
	const connectMarker = "RtdServer::ConnectData("
	cidx := strings.Index(src, connectMarker)
	if cidx < 0 {
		t.Fatalf("RtdServer::ConnectData not found in xll_rtd.cpp")
	}
	const disconnectMarker = "RtdServer::DisconnectData("
	didx := strings.Index(src, disconnectMarker)
	if didx < 0 || didx <= cidx {
		t.Fatalf("RtdServer::DisconnectData not found after ConnectData in xll_rtd.cpp")
	}
	connectBody := src[cidx:didx]

	for _, want := range []string{
		// A bounded retry loop exists (the drain-cap fix).
		"kMaxAttempts",
		// Short per-attempt timeout drives the loop (not a single long Send).
		"kAttemptTimeoutMs",
		// The Send result is inspected so a delivered ack ends the loop.
		"res.HasError()",
		"MSG_RTD_CONNECT, kAttemptTimeoutMs",
		// The retry honors the unload self-abort contract at the yield points.
		"g_isUnloading.load(std::memory_order_acquire)",
		// Each attempt re-acquires a fresh slot (Send disowns its slot on timeout).
		"g_phost->GetZeroCopySlot();",
	} {
		if !strings.Contains(connectBody, want) {
			t.Errorf("ConnectData missing %q\n---\n%s", want, connectBody)
		}
	}

	// The per-attempt timeout must be small enough that an in-flight Connect
	// observes g_isUnloading well within the 2000 ms drain cap. 250 ms is the
	// chosen value (mirrors the ribbon path's 200 ms class of short timeout).
	if !strings.Contains(connectBody, "kAttemptTimeoutMs = 250") {
		t.Errorf("ConnectData per-attempt timeout is not the expected 250ms; drain-cap margin assumption may be broken\n---\n%s", connectBody)
	}

	// The pre-fix code sent exactly once with a 5000 ms blocking timeout. That
	// single long Send is what created the UAF window; assert it is gone.
	if strings.Contains(connectBody, "MSG_RTD_CONNECT, 5000)") {
		t.Errorf("ConnectData still uses the old single-shot 5000ms blocking Send (drain-cap gap not closed)")
	}

	// Regression for the MinGW "'kAttemptTimeoutMs' is not captured" build break
	// (real-Excel smoketest, 2026-06-12): the constexpr retry constants are
	// odr-used inside the detached-thread lambda (passed by value to
	// std::chrono::milliseconds and slot.Send). When they were declared in the
	// ENCLOSING scope with a non-capturing lambda ([TopicID, strings, newVal]),
	// MinGW/GCC rejected the odr-use ("is not captured") and the XLL failed to
	// compile — the entire RTD path was unbuildable under the supported MinGW
	// toolchain. They MUST be declared inside the lambda body (after the
	// std::thread([...]() opener), which is how ribbon_addin.cpp::SendCommandInvoke
	// already does it). Assert the declaration sites come AFTER the thread opener.
	threadOpen := strings.Index(connectBody, "std::thread([")
	if threadOpen < 0 {
		t.Fatalf("ConnectData detached-thread lambda opener not found")
	}
	for _, decl := range []string{
		"constexpr int kMaxAttempts",
		"constexpr unsigned int kAttemptTimeoutMs",
	} {
		declIdx := strings.Index(connectBody, decl)
		if declIdx < 0 {
			t.Errorf("ConnectData missing constexpr declaration %q", decl)
			continue
		}
		if declIdx < threadOpen {
			t.Errorf("%q is declared OUTSIDE the detached-thread lambda (before std::thread([...]); "+
				"the constexpr is odr-used inside the non-capturing lambda, so MinGW/GCC fails to "+
				"compile with \"is not captured\". Move the declaration inside the lambda body.", decl)
		}
	}

	// DisconnectData: already 500 ms (< 2000 ms drain cap) so no retry loop is
	// required, but it must still re-check the unload flag and null-check
	// g_phost before touching SHM (forced-unload race guard).
	disconnectBody := src[didx:]
	for _, want := range []string{
		"g_isUnloading.load(std::memory_order_acquire)",
		"g_phost",
		"MSG_RTD_DISCONNECT, 500",
	} {
		if !strings.Contains(disconnectBody, want) {
			t.Errorf("DisconnectData missing %q\n---\n%s", want, disconnectBody)
		}
	}
}
