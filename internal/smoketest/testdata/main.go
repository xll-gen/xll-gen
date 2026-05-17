package main

import (
	"context"
	"time"

	"xll_smoke/generated"
)

type Service struct{}

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
