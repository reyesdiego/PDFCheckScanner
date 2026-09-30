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
	"sync"
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
//
// run is the only thing that sends on done, and it sends once, so there is
// nothing to race. The buffer is what lets the goroutine exit even when the
// test has stopped listening, and the cleanup cancels run however the test
// ends, so a failed test cannot leave a server running into the next one.
func runInBackground(t *testing.T, ctx context.Context, addr string) <-chan error {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	done := make(chan error, 1)
	go func() { done <- run(ctx, addr, quietLogger) }()
	return done
}

// waitUntilServing polls addr until the router answers. GET is not routed, so
// a 405 is the proof.
func waitUntilServing(t *testing.T, addr string) {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := client.Get("http://" + addr + "/detect")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Fatalf("GET /detect = %d, want 405", resp.StatusCode)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never answered: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// requirePortReleased fails if something still listens on addr.
func requirePortReleased(t *testing.T, addr string) {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("port still held after run returned: %v", err)
	}
	ln.Close()
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

	err = waitFor(t, runInBackground(t, context.Background(), taken.Addr().String()), 5*time.Second)
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
	done := runInBackground(t, ctx, addr)
	waitUntilServing(t, addr)

	cancel()
	if err := waitFor(t, done, 15*time.Second); err != nil {
		t.Fatalf("run = %v, want nil after a clean shutdown", err)
	}

	// The port is released and nothing run started is left behind.
	requirePortReleased(t, addr)
	requireGoroutinesBackTo(t, baseline)
}

// Several shutdown requests at once, as when signals arrive in a burst while
// run is already stopping, must stop the server exactly once and cleanly.
// Run with -race, as make check does, this is what would catch a data race
// between them.
func TestRunStopsOnceUnderConcurrentCancellation(t *testing.T) {
	addr := freeAddr(t)
	baseline := goroutineBaseline()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runInBackground(t, ctx, addr)
	waitUntilServing(t, addr)

	start := make(chan struct{})
	var cancellers sync.WaitGroup
	for range 16 {
		cancellers.Go(func() {
			<-start
			cancel()
		})
	}
	close(start)
	cancellers.Wait()

	if err := waitFor(t, done, 15*time.Second); err != nil {
		t.Fatalf("run = %v, want nil after a clean shutdown", err)
	}
	select {
	case err := <-done:
		t.Fatalf("run reported a second result: %v", err)
	default:
	}
	requirePortReleased(t, addr)
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

func TestParseMaxPixels(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    int
		wantErr bool
	}{
		{in: "", want: defaultMaxPixels},
		{in: "30000000", want: 30_000_000},
		{in: "30_000_000", want: 30_000_000},
		{in: "1000000", want: minMaxPixels},
		{in: "100000000", want: maxMaxPixels},
		{in: "999999", wantErr: true},
		{in: "100000001", wantErr: true},
		{in: "-5", wantErr: true},
		{in: "24M", wantErr: true},
	} {
		got, err := parseMaxPixels(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseMaxPixels(%q) = %d, want an error", tc.in, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("parseMaxPixels(%q) = %d, %v, want %d", tc.in, got, err, tc.want)
		}
	}
}
