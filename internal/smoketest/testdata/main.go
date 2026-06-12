package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/xll-gen/xll-gen/pkg/server"

	"xll_smoke/generated"
)

type Service struct{}

// SmokeCommand is invoked through the REAL ribbon dispatch path (ribbon
// onAction -> COM add-in IDispatch::GetIDsOfNames/Invoke -> SendCommandInvoke)
// OR via Application.Run. It cannot drive Excel back (no sugar in this minimal
// project), so it proves delivery by writing a sentinel file next to the
// server exe carrying the invoking Excel PID — which also exercises
// CommandContext.ExcelPID propagation. The harness reads this file to confirm
// the dispatch chain reached the Go handler.
func (s *Service) SmokeCommand(ctx context.Context, cmd server.CommandContext) error {
	dir := "."
	if exe, err := os.Executable(); err == nil {
		dir = filepath.Dir(exe)
	}
	path := filepath.Join(dir, "smoke_command.sentinel")
	content := fmt.Sprintf("command=%s controlId=%s excelPID=%d\n", cmd.CommandName, cmd.ControlID, cmd.ExcelPID)
	return os.WriteFile(path, []byte(content), 0o644)
}

func (s *Service) Add(ctx context.Context, a int32, b int32) (int32, error) {
	return a + b, nil
}

func (s *Service) AsyncAdd(ctx context.Context, a int32, b int32) (int32, error) {
	return a + b, nil
}

// RtdTick_RTD pushes a single value (v * 7) ~50ms after Excel subscribes.
// The smoke harness lowers Application.RTD.ThrottleInterval to 100ms so
// the result is observable within a couple of seconds.
func (s *Service) RtdTick_RTD(ctx context.Context, topicID int32, v int32) error {
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(50 * time.Millisecond):
		}
		_ = generated.PushRtdUpdate(topicID, v*7)
	}()
	return nil
}

func (s *Service) OnCalculationEnded(ctx context.Context) error  { return nil }
func (s *Service) OnCalculationCanceled(ctx context.Context) error { return nil }

func (s *Service) OnRtdConnect(ctx context.Context, topicID int32, strings []string, newValues bool) error {
	return nil
}
func (s *Service) OnRtdDisconnect(ctx context.Context, topicID int32) error { return nil }

func main() {
	generated.Serve(&Service{})
}
