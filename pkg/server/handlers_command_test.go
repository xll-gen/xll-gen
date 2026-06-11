package server

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/xll-gen/types/go/protocol"
)

func buildCommandInvoke(t *testing.T, name, controlID string) []byte {
	t.Helper()
	b := flatbuffers.NewBuilder(64)
	nameOff := b.CreateString(name)
	ctrlOff := b.CreateString(controlID)
	protocol.CommandInvokeRequestStart(b)
	protocol.CommandInvokeRequestAddCommandName(b, nameOff)
	protocol.CommandInvokeRequestAddControlId(b, ctrlOff)
	b.Finish(protocol.CommandInvokeRequestEnd(b))
	return b.FinishedBytes()
}

func newTestSysHandler() *SystemHandler {
	return NewSystemHandler(NewChunkManager(), NewAsyncBatcher(), NewCommandBatcher(), NewRefCache(), nil)
}

func parseInvokeResp(t *testing.T, respBuf []byte, n int32) *protocol.CommandInvokeResponse {
	t.Helper()
	if n <= 0 {
		t.Fatalf("no response written, n=%d", n)
	}
	return protocol.GetRootAsCommandInvokeResponse(respBuf[:n], 0)
}

func TestHandleCommandInvoke_RoutesAndAcks(t *testing.T) {
	h := newTestSysHandler()
	respBuf := make([]byte, 4096)
	b := flatbuffers.NewBuilder(256)

	var mu sync.Mutex
	var got CommandContext
	done := make(chan struct{})
	resolve := func(name string) (func(context.Context, CommandContext) error, bool) {
		if name != "RunReport" {
			return nil, false
		}
		return func(_ context.Context, cmd CommandContext) error {
			mu.Lock()
			got = cmd
			mu.Unlock()
			close(done)
			return nil
		}, true
	}

	data := buildCommandInvoke(t, "RunReport", "xllgen_btn_0_0")
	n, msgType := h.HandleCommandInvoke(data, respBuf, b, resolve)
	if msgType != MsgCommandInvoke {
		t.Fatalf("msgType: got %d want %d", msgType, MsgCommandInvoke)
	}
	resp := parseInvokeResp(t, respBuf, n)
	if !resp.Ok() {
		t.Fatalf("expected ok=true, error=%s", resp.Error())
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler not invoked within 2s")
	}
	mu.Lock()
	defer mu.Unlock()
	if got.CommandName != "RunReport" || got.ControlID != "xllgen_btn_0_0" {
		t.Errorf("CommandContext: %+v", got)
	}
}

func TestHandleCommandInvoke_UnknownCommand(t *testing.T) {
	h := newTestSysHandler()
	respBuf := make([]byte, 4096)
	b := flatbuffers.NewBuilder(256)
	resolve := func(string) (func(context.Context, CommandContext) error, bool) { return nil, false }

	n, _ := h.HandleCommandInvoke(buildCommandInvoke(t, "Nope", ""), respBuf, b, resolve)
	resp := parseInvokeResp(t, respBuf, n)
	if resp.Ok() {
		t.Fatal("expected ok=false for unknown command")
	}
	if !strings.Contains(string(resp.Error()), "Nope") {
		t.Errorf("error should name the command: %s", resp.Error())
	}
}

func TestHandleCommandInvoke_PanicRecovered(t *testing.T) {
	h := newTestSysHandler()
	respBuf := make([]byte, 4096)
	b := flatbuffers.NewBuilder(256)
	invoked := make(chan struct{})
	resolve := func(string) (func(context.Context, CommandContext) error, bool) {
		return func(context.Context, CommandContext) error {
			close(invoked)
			panic("boom")
		}, true
	}

	n, _ := h.HandleCommandInvoke(buildCommandInvoke(t, "X", ""), respBuf, b, resolve)
	resp := parseInvokeResp(t, respBuf, n)
	if !resp.Ok() {
		t.Fatal("delivery ack should be ok even if handler later panics")
	}
	select {
	case <-invoked:
	case <-time.After(2 * time.Second):
		t.Fatal("handler not invoked")
	}
	// Give the recover deferral a moment; the test passes if we don't crash.
	time.Sleep(50 * time.Millisecond)
}
