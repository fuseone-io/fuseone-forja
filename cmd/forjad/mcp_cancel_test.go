package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/claim"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/mcp"
)

// waitBound is how long a test waits for what a cancelled run does next.
const waitBound = 10 * time.Second

// servedFixture serves fx's server over HTTP the way serve does, with
// drain and the callers serve names, and returns its address, the cancel
// of serve's context and what serveMCPHTTP returned.
func servedFixture(t *testing.T, fx *cancelFixture, drain time.Duration) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bound, returned := make(chan string, 1), make(chan error, 1)
	cfg := httpConfig{addr: "127.0.0.1:0", clients: testRegistry(t), drain: drain}
	fx.deps.Callers = registryCallers{reg: cfg.clients}
	go func() {
		returned <- host{listen: capturingListen(bound)}.serveMCPHTTP(ctx, mcpHTTPServer(fx.deps), cfg, httpSinks{log: &lockedBuffer{}, ready: new(readiness)})
	}()
	return <-bound, cancel, returned
}

// implement calls forja.implement as a 2026-07-28 client on its own
// connection, which closes when ctx ends.
func implement(ctx context.Context, addr string) {
	body := toolCall("forja.implement", `{"repository":"`+cancelRepo+`","issue":`+strconv.Itoa(cancelIssue)+`,"base_branch":"main"}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+mcpPath, strings.NewReader(body))
	if err != nil {
		return
	}
	setCallHeaders(req, body)
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	if res, err := client.Do(req); err == nil {
		_ = res.Body.Close()
	}
}

func await(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(waitBound):
		t.Fatalf("%s did not happen within %v", what, waitBound)
	}
}

// stateOverHTTP is the run's state as forja.status answers it on a new
// connection of the same client. The client's connections close with it:
// a pooled one the server never saw a request on would hold the server's
// shutdown for the 5 s net/http waits on a new connection.
func stateOverHTTP(t *testing.T, addr, runID string) string {
	t.Helper()
	base := &http.Transport{}
	defer base.CloseIdleConnections()
	cs, err := connectHTTP(t, "http://"+addr+mcpPath, testBearer, base)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = cs.Close() }()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "forja.status", Arguments: map[string]any{"run_id": runID}})
	if err != nil || res.IsError {
		t.Fatalf("forja.status: %v %+v", err, res)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("forja.status output %s: %v", raw, err)
	}
	return out.State
}

// A 2026-07-28 client that closes its connection during forja.implement
// stops the run: the workspace is destroyed, the claim released and the
// run recorded failed.
func TestServeMCPHTTP_clientDropsDuringImplement_runTornDownAndFailed(t *testing.T) {
	t.Parallel()
	fx := newCancelFixture(t)
	addr, stop, returned := servedFixture(t, fx, 5*time.Second)
	callCtx, drop := context.WithCancel(context.Background())
	t.Cleanup(drop)
	go implement(callCtx, addr)
	await(t, fx.ws.entered, "the agent starting")
	drop()
	await(t, fx.ws.closed, "the workspace closing")
	await(t, fx.claims.released, "the claim release")
	deadline := time.Now().Add(waitBound)
	state := stateOverHTTP(t, addr, cancelRunID)
	for state != string(domain.StateFailed) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		state = stateOverHTTP(t, addr, cancelRunID)
	}
	if state != string(domain.StateFailed) {
		t.Fatalf("forja.status state %q after the caller left, want failed", state)
	}
	stop()
	if err := <-returned; err != nil {
		t.Fatalf("serveMCPHTTP = %v, want a clean stop", err)
	}
}

// A stop during forja.implement cancels the run and waits for its
// teardown: by the time serveMCPHTTP returns, cleanly, the workspace is
// gone, the claim released and the run failed.
func TestServeMCPHTTP_stoppedDuringImplement_runTornDownBeforeReturn(t *testing.T) {
	t.Parallel()
	fx := newCancelFixture(t)
	addr, stop, returned := servedFixture(t, fx, 5*time.Second)
	go implement(context.Background(), addr)
	await(t, fx.ws.entered, "the agent starting")
	stop()
	var err error
	select {
	case err = <-returned:
	case <-time.After(waitBound):
		t.Fatalf("serveMCPHTTP did not return within %v", waitBound)
	}
	if err != nil {
		t.Fatalf("serveMCPHTTP = %v, want a clean stop once the run was torn down", err)
	}
	run, getErr := fx.runs.Get(context.Background(), cancelRunID)
	if !fx.ws.halted.Load() || !fx.ws.isClosed() || fx.claims.isHeld() || getErr != nil || run.State != domain.StateFailed {
		t.Fatalf("at return: workspace halted %v, closed %v, claim held %v, run %q (%v); want halted, closed, released, failed",
			fx.ws.halted.Load(), fx.ws.isClosed(), fx.claims.isHeld(), run.State, getErr)
	}
}

// The drain outlasts the longest teardown of a run whose caller left: the
// claim settling a mutation whose answer was lost, then every record and
// the release, one after the other, each at its bound. The stop waits for
// it instead of cutting it off.
func TestMCPHTTPConfig_drain_outlastsTheTeardownBound(t *testing.T) {
	t.Parallel()
	cfg, err := mcpHTTPConfig(envOf(map[string]string{EnvMCPAddr: "127.0.0.1:0", EnvClientsFile: writeFile(t, "c.yaml", clientsYAML())}))
	teardown := claim.SettleBound + mcp.CancelledRunRecords*mcp.FinishTimeout
	if err != nil || cfg.drain <= teardown {
		t.Fatalf("mcpHTTPConfig = (drain %v, %v), want a drain over the %v teardown", cfg.drain, err, teardown)
	}
}
