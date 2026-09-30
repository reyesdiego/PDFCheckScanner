package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
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

// goroutineBaseline counts running goroutines once os/signal has started its
// watcher, which it does on first use and never stops. TestShutdownOnSignal
// starts it, so without this the run tests would pass or fail by test order.
func goroutineBaseline() int {
	_, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	stop()
	return runtime.NumGoroutine()
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
	baseline := goroutineBaseline()

	err = waitFor(t, runInBackground(context.Background(), taken.Addr().String()), 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("err = %v, want a listen error", err)
	}
	requireGoroutinesBackTo(t, baseline)
}

func TestRunStopsCleanlyWhenCancelled(t *testing.T) {
	addr := freeAddr(t)
	baseline := goroutineBaseline()

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

// main relies on this to shut down: a container stop is a SIGTERM, Ctrl-C is
// an Interrupt, and either must cancel the context run is given. The process
// signals itself, which the handler catches, so the test binary survives.
func TestShutdownOnSignal(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			ctx, stop := shutdownOnSignal(context.Background())
			defer stop()

			if err := syscall.Kill(os.Getpid(), sig); err != nil {
				t.Fatal(err)
			}
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
				t.Fatalf("%s did not cancel the context", sig)
			}
		})
	}
}
