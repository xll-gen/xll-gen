package msgid

import "testing"

// TestMessageIDValues pins the numeric message-ID values to the authoritative
// C++ mirror in internal/assets/files/include/xll_ipc.h (the MSG_* #defines).
// A drift here means the Go server and the XLL host would disagree on the wire,
// so this test fails loudly if a value is changed without a coordinated mirror
// update (AGENTS.md §18.6).
func TestMessageIDValues(t *testing.T) {
	cases := []struct {
		name string
		got  int
		want int
	}{
		{"MsgAck", MsgAck, 2},
		{"MsgBatchAsyncResponse", MsgBatchAsyncResponse, 128},
		{"MsgChunk", MsgChunk, 129},
		{"MsgSetRefCache", MsgSetRefCache, 130},
		{"MsgCalculationEnded", MsgCalculationEnded, 131},
		{"MsgCalculationCanceled", MsgCalculationCanceled, 132},
		{"MsgRtdConnect", MsgRtdConnect, 133},
		{"MsgRtdDisconnect", MsgRtdDisconnect, 134},
		{"MsgRtdUpdate", MsgRtdUpdate, 135},
		{"MsgRtdHeartbeat", MsgRtdHeartbeat, 136},
		{"MsgCommandInvoke", MsgCommandInvoke, 137},
		{"MsgUserStart", MsgUserStart, 140},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d (mirror: xll_ipc.h MSG_* #defines)", c.name, c.got, c.want)
		}
	}
}
