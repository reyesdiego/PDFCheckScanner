package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"
)

var quietLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// freeAddr finds a loopback port nothing is listening on.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// runInBackground starts run and returns a channel that gets its result.
func runInBackground(ctx context.Context, addr string) <-chan error {
	done := make(chan error, 1)
	go func() { done <- run(ctx, addr, quietLogger) }()
	return done
}

func waitFor(t *testing.T, done <-chan error, within time.Duration) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(within):
		t.Fatalf("run did not return within %s", within)
		return nil
	}
}

// requireGoroutinesBackTo fails if goroutines started during the test are
// still running, allowing a moment for exiting ones to be reaped.
func requireGoroutinesBackTo(t *testing.T, baseline int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			t.Fatalf("%d goroutines still running, want at most %d",
				runtime.NumGoroutine(), baseline)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRunFailsFastWhenThePortIsTaken(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	baseline := runtime.NumGoroutine()

	err = waitFor(t, runInBackground(context.Background(), taken.Addr().String()), 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("err = %v, want a listen error", err)
	}
	requireGoroutinesBackTo(t, baseline)
}

func TestRunStopsCleanlyWhenCancelled(t *testing.T) {
	addr := freeAddr(t)
	baseline := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runInBackground(ctx, addr)

	// Serving means answering: GET is not routed, so a 405 is the proof.
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := client.Get("http://" + addr + "/detect")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("GET /detect = %d, want 405", resp.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never answered: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	if err := waitFor(t, done, 15*time.Second); err != nil {
		t.Fatalf("run = %v, want nil after a clean shutdown", err)
	}

	// The port is released and nothing run started is left behind.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("port still held after run returned: %v", err)
	}
	ln.Close()
	requireGoroutinesBackTo(t, baseline)
}
