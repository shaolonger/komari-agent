package server

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestControlWorkerTrackerWaitsAndHonorsDeadline(t *testing.T) {
	tracker := newControlWorkerTracker()
	release := make(chan struct{})
	tracker.launch(func() { <-release })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := tracker.wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait() error = %v, want deadline exceeded", err)
	}
	close(release)
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := tracker.wait(ctx); err != nil {
		t.Fatalf("wait after release: %v", err)
	}
}

func TestControlWorkerTrackerHandlesMultipleGenerations(t *testing.T) {
	tracker := newControlWorkerTracker()
	for range 10 {
		tracker.launch(func() {})
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := tracker.wait(ctx); err != nil {
		t.Fatalf("wait first generation: %v", err)
	}
	tracker.launch(func() {})
	if err := tracker.wait(ctx); err != nil {
		t.Fatalf("wait second generation: %v", err)
	}
}
