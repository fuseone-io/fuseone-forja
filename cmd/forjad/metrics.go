package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

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

// startMetrics serves deps' runs at deps' prices on GET /metrics at addr
// until stop is called or ctx ends; stop returns once the server is down.
// An empty addr serves nothing. A listener that cannot be opened is an
// error naming the variable; a server that fails later is reported on
// stderr and leaves the MCP server running.
func (h host) startMetrics(ctx context.Context, stderr io.Writer, addr string, deps mcp.Deps) (stop func(), err error) {
	if addr == "" {
		return func() {}, nil
	}
	ln, err := h.listen(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EnvMetricsAddr, err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", metrics.Handler(deps.Runs, deps.Prices))
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
	return func() { cancel(); <-done }, nil
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
