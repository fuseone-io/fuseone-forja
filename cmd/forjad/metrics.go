package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/mcp"
	"github.com/fuseone-io/fuseone-forja/internal/metrics"
)

// EnvMetricsAddr is the host:port serve exposes the run metrics on. Unset,
// serve opens no listener.
const EnvMetricsAddr = "FORJA_METRICS_ADDR"

// The metrics server's bounds: how long a scraper may take to send its
// headers, and how long a scrape in progress may finish once serve stops.
const (
	metricsReadHeaderTimeout = 5 * time.Second
	metricsShutdownTimeout   = 5 * time.Second
)

// listenFunc opens the listener the metrics are served on.
type listenFunc func(ctx context.Context, addr string) (net.Listener, error)

func listenTCP(ctx context.Context, addr string) (net.Listener, error) {
	return new(net.ListenConfig).Listen(ctx, "tcp", addr)
}

// host is what serve reaches outside the process: its environment and the
// network.
type host struct {
	getenv func(string) string
	listen listenFunc
}

// metricsAddr is the configured address, empty when unset; one that is not
// host:port is an error naming the variable.
func metricsAddr(getenv func(string) string) (string, error) {
	addr := getenv(EnvMetricsAddr)
	if addr == "" {
		return "", nil
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return "", fmt.Errorf("%s: %w", EnvMetricsAddr, err)
	}
	return addr, nil
}

// readiness is what GET /readyz answers. Serve opens the listener only after
// every mandatory dependency passed its startup validation, so the one state
// left to report is whether the MCP session is serving its transport. It
// probes nothing and answers nothing about runs, clients or repositories.
type readiness struct {
	serving atomic.Bool
}

func (r *readiness) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	if !r.serving.Load() {
		probeReply(w, http.StatusServiceUnavailable, "not ready\n")
		return
	}
	probeReply(w, http.StatusOK, "ready\n")
}

// healthz answers while the process can answer at all.
func healthz(w http.ResponseWriter, _ *http.Request) {
	probeReply(w, http.StatusOK, "ok\n")
}

func probeReply(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	// A prober that went away has nothing left to read.
	_, _ = io.WriteString(w, body)
}

// serveMCP runs server over transport as sdk.Server.Run does, and holds
// ready from the moment the session reads the transport until it ends.
func serveMCP(ctx context.Context, server *sdk.Server, transport sdk.Transport, ready *readiness) error {
	ss, err := server.Connect(ctx, transport, nil)
	if err != nil {
		return err
	}
	ready.serving.Store(true)
	defer ready.serving.Store(false)
	ended := make(chan error, 1)
	go func() { ended <- ss.Wait() }()
	select {
	case err := <-ended:
		return err
	case <-ctx.Done():
		_ = ss.Close()
		<-ended
		return ctx.Err()
	}
}

// startMetrics serves deps' runs at deps' prices on GET /metrics at addr,
// with GET /healthz and GET /readyz next to them, until stop is called or
// ctx ends; stop returns once the server is down. ready is what /readyz
// reports and is usable even when addr is empty and nothing is served. A
// listener that cannot be opened is an error naming the variable; a server
// that fails later is reported on stderr and leaves the MCP server running.
func (h host) startMetrics(ctx context.Context, stderr io.Writer, addr string, deps mcp.Deps) (stop func(), ready *readiness, err error) {
	ready = new(readiness)
	if addr == "" {
		return func() {}, ready, nil
	}
	ln, err := h.listen(ctx, addr)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", EnvMetricsAddr, err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler(deps.Runs, deps.Prices))
	mux.HandleFunc("GET /healthz", healthz)
	mux.Handle("GET /readyz", ready)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: metricsReadHeaderTimeout}
	ctx, cancel := context.WithCancel(ctx)
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := awaitMetrics(ctx, srv, served); err != nil {
			// The MCP server keeps running, so a failed stderr leaves no
			// channel to report on.
			_, _ = fmt.Fprintf(stderr, "forjad: metrics: %v\n", err)
		}
	}()
	return func() { cancel(); <-done }, ready, nil
}

// awaitMetrics shuts srv down when ctx ends and waits for Serve to return;
// it returns Serve's error unless that is the shutdown itself.
func awaitMetrics(ctx context.Context, srv *http.Server, served <-chan error) error {
	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}
	drain, cancel := context.WithTimeout(context.WithoutCancel(ctx), metricsShutdownTimeout)
	defer cancel()
	_ = srv.Shutdown(drain)
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
