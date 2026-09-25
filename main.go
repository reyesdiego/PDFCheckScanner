package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
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
	if err := run(context.Background(), *addr, logger); err != nil {
		logger.Error("server failed", "err", err)
		os.Exit(1)
	}
}

// run serves on addr until the server fails or ctx is cancelled or the process
// is interrupted, then gives in-flight requests 10s to finish.
func run(ctx context.Context, addr string, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

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

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	logger.Info("listening", "addr", addr)

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("listen: %w", err)
	case <-ctx.Done():
		logger.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
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
