package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestShutdownClosesDatabase(t *testing.T) {
	closed := false
	if err := shutdown(context.Background(), func() { closed = true }); err != nil {
		t.Fatal(err)
	}
	if !closed {
		t.Fatal("database not closed")
	}
}
func TestShutdownDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	release := make(chan struct{})
	defer close(release)
	if err := shutdown(ctx, func() { <-release }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown not bounded: %v", err)
	}
}
