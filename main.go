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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func main() {
	addr := flag.String("addr", ":8080", "address to listen on")
	pixels := flag.String("max-pixels", os.Getenv("MAX_PIXELS"),
		fmt.Sprintf("largest image to accept, in pixels (default %d; env MAX_PIXELS)", defaultMaxPixels))
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	limit, err := parseMaxPixels(*pixels)
	if err != nil {
		logger.Error("bad configuration", "err", err)
		os.Exit(2)
	}
	maxPixels = limit

	// stop runs before os.Exit, which would skip a deferred call.
	ctx, stop := shutdownOnSignal(context.Background())
	err = run(ctx, *addr, logger)
	stop()
	if err != nil {
		logger.Error("server failed", "err", err)
		os.Exit(1)
	}
}

// Bounds on a configured maxPixels. Below the minimum a letter page cannot be
// rendered even at minRasterDPI, so every scanned PDF would come back empty;
// above the maximum one decode can take 400 MB.
const (
	minMaxPixels = 1_000_000
	maxMaxPixels = 100_000_000
)

// parseMaxPixels reads a -max-pixels value, where empty means the default.
func parseMaxPixels(s string) (int, error) {
	if s == "" {
		return defaultMaxPixels, nil
	}
	n, err := strconv.Atoi(strings.ReplaceAll(s, "_", ""))
	if err != nil {
		return 0, fmt.Errorf("max pixels %q is not a whole number", s)
	}
	if n < minMaxPixels || n > maxMaxPixels {
		return 0, fmt.Errorf("max pixels %d is outside %d-%d", n, minMaxPixels, maxMaxPixels)
	}
	return n, nil
}

// shutdownOnSignal returns a context that the first Interrupt or SIGTERM
// cancels, which is what tells run to shut down. Only the first is caught:
// once ctx is done, signals get their default behaviour back, so a second one
// ends the process at once instead of waiting out the shutdown grace period.
func shutdownOnSignal(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	context.AfterFunc(ctx, stop)
	return ctx, stop
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

// newRouter wires the middleware, the detection endpoint and the upload page
// that calls it. The 30s timeout bounds each request, including the PDF
// rasterization it can trigger.
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

	r.Get("/", serveUI("index.html", "text/html; charset=utf-8"))
	r.Get("/app.js", serveUI("app.js", "text/javascript; charset=utf-8"))
	r.Get("/style.css", serveUI("style.css", "text/css; charset=utf-8"))
	r.Get("/favicon.svg", serveUI("favicon.svg", "image/svg+xml"))
	return r
}
