package main

import (
	"context"
	"fmt"
	"MyTest/generated"
	"time"
)

type MyService struct{}

func (s *MyService) Add(ctx context.Context, a int32, b int32) (int32, error) {
	return a + b, nil
}

func (s *MyService) GetPrice(ctx context.Context, ticker string) (float64, error) {
	// Simulate async work
	select {
	case <-time.After(100 * time.Millisecond):
		return 123.45, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (s *MyService) Greet(ctx context.Context, name string) (string, error) {
	return fmt.Sprintf("Hello, %s!", name), nil
}

func (s *MyService) IsEven(ctx context.Context, val int32) (bool, error) {
	return val%2 == 0, nil
}

func main() {
	// Connects to SHM and starts processing
	generated.Serve(&MyService{})
}
