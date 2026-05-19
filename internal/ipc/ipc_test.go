package ipc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestServeAndFetch(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "status.sock")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	provider := func() Status {
		calls.Add(1)
		return Status{PID: 42, AuthUser: "tester"}
	}

	errCh := make(chan error, 1)
	go func() { errCh <- Serve(ctx, sock, provider) }()

	// Wait for the socket to bind. Serve does the listen synchronously,
	// but it runs in a goroutine here, so we poll.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket never appeared")
		}
		time.Sleep(20 * time.Millisecond)
	}

	s, err := Fetch(ctx, sock)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if s.PID != 42 || s.AuthUser != "tester" {
		t.Errorf("unexpected status: %+v", s)
	}
	if calls.Load() != 1 {
		t.Errorf("provider calls: %d", calls.Load())
	}

	cancel()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
}

func TestFetchNoSocket(t *testing.T) {
	_, err := Fetch(context.Background(), filepath.Join(t.TempDir(), "nope.sock"))
	if !errors.Is(err, ErrNotRunning) {
		t.Errorf("expected ErrNotRunning, got %v", err)
	}
}
