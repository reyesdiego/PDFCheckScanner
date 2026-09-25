package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func main() {
	addr := flag.String("addr", ":8080", "address to listen on")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	// Interrupt and SIGTERM cancel ctx, which is what tells run to shut down.
	// stop releases the signal handler, and runs before os.Exit, which would
	// skip a deferred call.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, *addr, logger)
	stop()
	if err != nil {
		logger.Error("server failed", "err", err)
		os.Exit(1)
	}
}

// run serves on addr until the server fails or ctx is cancelled, then gives
// in-flight requests 10s to finish. It returns only once the server has fully
// stopped, so nothing it started outlives it.
func run(ctx context.Context, addr string, logger *slog.Logger) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           newRouter(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// PDFs without form fields need an external rasterizer; say so at boot
	// rather than only when the first scanned PDF arrives.
	if _, err := exec.LookPath(rasterizer); err != nil {
		logger.Warn("rasterizer missing: scanned PDFs will be rejected",
			"binary", rasterizer)
	}

	// Binding here rather than inside ListenAndServe makes a failure to start,
	// such as the port being taken, an immediate error before anything else
	// is running, and means "listening" is only logged once it is true.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	logger.Info("listening", "addr", ln.Addr().String())

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case err := <-serveErr:
		// Serve closes the listener itself when it fails.
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shutdownErr := srv.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		// Requests outlived the grace period; cut their connections.
		srv.Close()
	}
	// Either way Serve now returns ErrServerClosed. Waiting for it means the
	// serving goroutine has exited by the time run does.
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return errors.Join(shutdownErr, fmt.Errorf("serve: %w", err))
	}
	if shutdownErr != nil {
		return fmt.Errorf("shutdown: %w", shutdownErr)
	}
	return nil
}

// detector is stateless and safe for concurrent use, so one instance serves
// every request.
var detector = NewDetector()

// newRouter wires the middleware and the one route the service exposes. The
// 30s timeout bounds each request, including the PDF rasterization it can
// trigger.
func newRouter() *chi.Mux {
	r := chi.NewRouter()
	r.Use(
		middleware.RequestID,
		middleware.RealIP,
		middleware.Logger,
		middleware.Recoverer,
		middleware.Timeout(30*time.Second),
	)
	r.Post("/detect", handleDetect)
	return r
}
