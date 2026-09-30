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
	"sync"
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

// Shutdown timeouts. In-flight requests get shutdownGrace to finish on their
// own. Past that their connections are cut, which cancels their contexts, and
// they get drainGrace more to notice and return.
const (
	shutdownGrace = 10 * time.Second
	drainGrace    = 5 * time.Second
)

// run serves on addr until the server fails or ctx is cancelled, then shuts
// down within shutdownGrace plus drainGrace. It returns only once the server
// has stopped and its handlers have returned, so nothing it started outlives
// it.
func run(ctx context.Context, addr string, logger *slog.Logger) error {
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

	return serve(ctx, ln, newRouter(), logger, shutdownGrace, drainGrace)
}

// serve runs h on ln until the server fails or ctx is cancelled. It takes the
// timeouts as arguments so tests can use short ones.
func serve(ctx context.Context, ln net.Listener, h http.Handler, logger *slog.Logger, grace, drain time.Duration) error {
	// Server.Close cuts connections but does not wait for the handlers
	// serving them, and a handler mid-detection holds OpenCV Mats, which are
	// C memory the garbage collector cannot see. Counting them is what lets
	// serve wait for them to let go.
	var handlers inFlight
	srv := &http.Server{
		Handler:           handlers.track(h),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case err := <-serveErr:
		// Serve closes the listener itself when it fails, but not the
		// connections it already accepted, whose handlers would carry on
		// after serve returned.
		srv.Close()
		return errors.Join(fmt.Errorf("serve: %w", err), handlers.wait(drain))
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	shutdownErr := srv.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		// Requests outlived the grace period; cut their connections, which
		// cancels their contexts, and give them a moment to return.
		srv.Close()
		if err := handlers.wait(drain); err != nil {
			shutdownErr = errors.Join(shutdownErr, err)
		}
	}
	// Either way Serve now returns ErrServerClosed. Waiting for it means the
	// serving goroutine has exited by the time serve does.
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return errors.Join(shutdownErr, fmt.Errorf("serve: %w", err))
	}
	if shutdownErr != nil {
		return fmt.Errorf("shutdown: %w", shutdownErr)
	}
	return nil
}

// inFlight counts the handlers that are running. It is not a sync.WaitGroup
// because a connection accepted just before shutdown can start a handler
// after the wait has begun, and a WaitGroup forbids an Add that races a Wait.
type inFlight struct {
	mu sync.Mutex
	n  int
	// changed is closed, and replaced, every time a handler returns.
	changed chan struct{}
}

// track wraps h so that each request is counted while it runs.
func (f *inFlight) track(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.n++
		f.mu.Unlock()
		defer f.done()
		h.ServeHTTP(w, r)
	})
}

func (f *inFlight) done() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n--
	if f.changed != nil {
		close(f.changed)
		f.changed = nil
	}
}

// wait blocks until no handler is running, or reports how many still are
// after timeout. A handler that ignores its cancelled context must not be
// able to hold the process open forever.
func (f *inFlight) wait(timeout time.Duration) error {
	deadline := time.After(timeout)
	for {
		f.mu.Lock()
		n := f.n
		if n == 0 {
			f.mu.Unlock()
			return nil
		}
		if f.changed == nil {
			f.changed = make(chan struct{})
		}
		changed := f.changed
		f.mu.Unlock()

		select {
		case <-changed:
		case <-deadline:
			return fmt.Errorf("%d requests still running after %s", n, timeout)
		}
	}
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
	r.Post("/preview", handlePreview)

	r.Get("/", serveUI("index.html", "text/html; charset=utf-8"))
	r.Get("/app.js", serveUI("app.js", "text/javascript; charset=utf-8"))
	r.Get("/style.css", serveUI("style.css", "text/css; charset=utf-8"))
	r.Get("/favicon.svg", serveUI("favicon.svg", "image/svg+xml"))
	return r
}
