package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/mcp"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

// refuseListen fails the test if serve opens any listener.
func refuseListen(t *testing.T) listenFunc {
	return func(_ context.Context, addr string) (net.Listener, error) {
		t.Errorf("serve listened on %q", addr)
		return nil, errors.New("no listener in this test")
	}
}

// capturingListen listens for real and hands the bound address over.
func capturingListen(bound chan<- string) listenFunc {
	return func(ctx context.Context, addr string) (net.Listener, error) {
		ln, err := listenTCP(ctx, addr)
		if err == nil {
			bound <- ln.Addr().String()
		}
		return ln, err
	}
}

func envOf(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

func TestServe_metricsAddrUnset_noListener(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, serverTransport := sdk.NewInMemoryTransports()

	var stderr bytes.Buffer
	h := host{getenv: quietEnv, listen: refuseListen(t)}
	if code := h.serve(ctx, serverTransport, &stderr); code != 0 {
		t.Fatalf("serve = %d, want 0: %s", code, stderr.String())
	}
}

func TestServe_malformedMetricsAddr_exitsOneNamingTheVariable(t *testing.T) {
	t.Parallel()
	taken, err := listenTCP(context.Background(), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = taken.Close() })

	tests := []struct {
		name, addr string
		listen     listenFunc
	}{
		{name: "no port", addr: "127.0.0.1", listen: refuseListen(t)},
		{name: "not an address", addr: "metrics please", listen: refuseListen(t)},
		{name: "port already bound", addr: taken.Addr().String(), listen: listenTCP},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, serverTransport := sdk.NewInMemoryTransports()
			env := map[string]string{EnvWorkspaceRuntime: "runc", EnvMetricsAddr: tt.addr}

			var stderr bytes.Buffer
			h := host{getenv: envOf(env), listen: tt.listen}
			if code := h.serve(ctx, serverTransport, &stderr); code != 1 {
				t.Fatalf("serve with %s=%q = %d, want 1", EnvMetricsAddr, tt.addr, code)
			}
			if !strings.Contains(stderr.String(), EnvMetricsAddr) {
				t.Fatalf("stderr %q does not name %s", stderr.String(), EnvMetricsAddr)
			}
		})
	}
}

func get(t *testing.T, url string) (*http.Response, string, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	return res, string(body), err
}

// servedMetrics starts serve with the metrics address and the price table
// set, and returns the bound address, the client session, the cancel of
// serve's context and its exit code.
func servedMetrics(t *testing.T) (string, *sdk.ClientSession, context.CancelFunc, <-chan int) {
	t.Helper()
	env := map[string]string{
		EnvWorkspaceRuntime: "runc", EnvMetricsAddr: "127.0.0.1:0",
		EnvPriceInput: "1", EnvPriceCacheWrite: "1", EnvPriceCacheRead: "1", EnvPriceOutput: "1", EnvPriceComputeMinute: "1",
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	clientTransport, serverTransport := sdk.NewInMemoryTransports()
	bound := make(chan string, 1)
	exited := make(chan int, 1)
	var stderr bytes.Buffer
	go func() { exited <- host{getenv: envOf(env), listen: capturingListen(bound)}.serve(ctx, serverTransport, &stderr) }()

	var addr string
	select {
	case addr = <-bound:
	case code := <-exited:
		t.Fatalf("serve exited %d before listening: %s", code, stderr.String())
	}
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "forjad-test-client", Version: "test"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	return addr, cs, cancel, exited
}

func TestServe_metricsAddrSet_scrapeServesRuns(t *testing.T) {
	t.Parallel()
	addr, cs, cancel, exited := servedMetrics(t)
	defer func() { _ = cs.Close() }()

	res, body, err := get(t, "http://"+addr+"/metrics")
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("scrape = %d %q, want 200 in the text format", res.StatusCode, res.Header.Get("Content-Type"))
	}
	// The cost family is there only when serve passed the price table on.
	for _, want := range []string{"# TYPE forja_runs gauge\n", "# TYPE forja_estimated_cost_usd_micros gauge\n"} {
		if !strings.Contains(body, want) {
			t.Fatalf("exposition lacks %q:\n%s", want, body)
		}
	}

	cancel()
	if code := <-exited; code != 0 {
		t.Fatalf("serve after cancel = %d, want 0", code)
	}
	if _, _, err := get(t, "http://"+addr+"/metrics"); err == nil {
		t.Fatal("the metrics listener still answers after serve returned")
	}
}

// The client going away ends the MCP server; the listener goes with it,
// although nobody cancelled serve's context.
func TestServe_transportClosed_metricsListenerClosed(t *testing.T) {
	t.Parallel()
	addr, cs, _, exited := servedMetrics(t)

	_ = cs.Close()
	if code := <-exited; code != 0 {
		t.Fatalf("serve after the client closed = %d, want 0", code)
	}
	if _, _, err := get(t, "http://"+addr+"/metrics"); err == nil {
		t.Fatal("the metrics listener still answers after serve returned")
	}
}

// The listener serves the server's own run store, and only at /metrics.
func TestStartMetrics_servesTheDepsRunStore(t *testing.T) {
	t.Parallel()
	store := runstore.NewMemory()
	finished := time.Date(2026, 11, 2, 12, 1, 0, 0, time.UTC)
	run := domain.Run{ID: "r1", Tool: domain.ToolImplement, Repository: "acme/api", State: domain.StateSucceeded,
		StartedAt: finished.Add(-time.Minute), FinishedAt: &finished}
	if err := store.Put(context.Background(), run); err != nil {
		t.Fatalf("Put: %v", err)
	}
	bound := make(chan string, 1)
	var stderr bytes.Buffer
	stop, err := host{listen: capturingListen(bound)}.startMetrics(context.Background(), &stderr, "127.0.0.1:0", mcp.Deps{Runs: store})
	if err != nil {
		t.Fatalf("startMetrics: %v", err)
	}
	defer stop()
	addr := <-bound

	_, body, err := get(t, "http://"+addr+"/metrics")
	if err != nil || !strings.Contains(body, `forja_runs{repository="acme/api",state="succeeded"} 1`+"\n") {
		t.Fatalf("scrape = %v:\n%s\nwant the store's succeeded run", err, body)
	}
	if strings.Contains(body, "forja_estimated_cost_usd_micros") {
		t.Fatal("a cost metric with no price table")
	}
	if res, _, err := get(t, "http://"+addr+"/"); err != nil || res.StatusCode != http.StatusNotFound {
		t.Fatalf("GET / = %v, %v; want 404", res, err)
	}
}
