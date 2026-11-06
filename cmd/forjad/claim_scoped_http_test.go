package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// pilotRun is a run of fuseone-pilot, the client testBearer names; the
// tests call as fuseone-staging, stagedBearer's, to see what of it reaches
// another client.
const pilotRun = "run-of-pilot"

// scopedServer serves fx over HTTP as serve does, its store sealed, and
// returns its address and the operator's log: every call, and what the
// sealed store reports.
func scopedServer(t *testing.T, fx *cancelFixture) (string, *lockedBuffer) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	bound, returned := make(chan string, 1), make(chan error, 1)
	cfg := httpConfig{addr: "127.0.0.1:0", clients: testRegistry(t), drain: time.Second}
	logs := &lockedBuffer{}
	fx.deps.Callers, fx.deps.Runs = registryCallers{reg: cfg.clients}, sealRuns(fx.deps.Runs, logs)
	go func() {
		returned <- host{listen: capturingListen(bound)}.serveMCPHTTP(ctx, mcpHTTPServer(fx.deps), cfg, httpSinks{log: logs, ready: new(readiness)})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-returned; err != nil {
			t.Errorf("serveMCPHTTP = %v, want a clean stop", err)
		}
	})
	return <-bound, logs
}

// callAs calls tool with args as the client bearer names, on its own
// connection, and returns the answer as the wire carries it.
func callAs(t *testing.T, addr, bearer, tool string, args map[string]any) string {
	t.Helper()
	base := &http.Transport{}
	defer base.CloseIdleConnections()
	cs, err := connectHTTP(t, "http://"+addr+mcpPath, bearer, base)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = cs.Close() }()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error %v", tool, err)
	}
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// pilotRecorded records pilotRun as fuseone-pilot's.
func pilotRecorded(t *testing.T, fx *cancelFixture) {
	t.Helper()
	run := domain.Run{ID: pilotRun, Tool: domain.ToolImplement, Repository: cancelRepo, Issue: cancelIssue, Client: "fuseone-pilot",
		State: domain.StateRunning, StartedAt: utcNow()}
	if err := fx.runs.Put(context.Background(), run); err != nil {
		t.Fatal(err)
	}
}

// pilotHoldsTheIssue records pilotRun and has it hold the issue the tests
// implement.
func pilotHoldsTheIssue(t *testing.T, fx *cancelFixture) {
	t.Helper()
	pilotRecorded(t, fx)
	now := utcNow()
	fx.claims.held[claimKey(cancelRepo, cancelIssue)] = domain.Claim{RunID: pilotRun, Repository: cancelRepo, Issue: cancelIssue, SHA: cancelBaseSHA, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
}

var implementArgs = map[string]any{"repository": cancelRepo, "issue": cancelIssue, "base_branch": "main"}

// Over HTTP, a claim held by fuseone-pilot's run refuses fuseone-staging
// with the repository and the issue and no run id, in its answer and in
// the log of its call; fuseone-pilot's own retry is told its run.
func TestServeHTTP_claimHeldByAnotherClient_holderNamedOnlyToItsOwner(t *testing.T) {
	t.Parallel()
	fx := newCancelFixture(t)
	pilotHoldsTheIssue(t, fx)
	addr, logs := scopedServer(t, fx)

	staging := callAs(t, addr, stagedBearer, "forja.implement", implementArgs)
	if !strings.Contains(staging, `"isError":true`) || !strings.Contains(staging, cancelRepo) || strings.Contains(staging, pilotRun) || strings.Contains(staging, "fuseone-pilot") {
		t.Fatalf("fuseone-staging got %s\nwant a refusal naming %s and neither %s nor its client", staging, cancelRepo, pilotRun)
	}
	if logged := logs.String(); !strings.Contains(logged, "client=fuseone-staging") || strings.Contains(logged, pilotRun) {
		t.Fatalf("operator log after fuseone-staging's call %q: want its call logged without %s", logged, pilotRun)
	}
	if pilot := callAs(t, addr, testBearer, "forja.implement", implementArgs); !strings.Contains(pilot, `"isError":true`) || !strings.Contains(pilot, pilotRun) {
		t.Fatalf("fuseone-pilot got %s\nwant a refusal naming its own %s", pilot, pilotRun)
	}
}

// Over HTTP, fuseone-staging's run recorded as having taken over
// fuseone-pilot's expired claim is answered without supersedes, and the
// log of the call does not name the run it took over.
func TestServeHTTP_supersededAnotherClientsRun_statusAndLogDoNotNameIt(t *testing.T) {
	t.Parallel()
	fx := newCancelFixture(t)
	pilotRecorded(t, fx)
	run := domain.Run{ID: "run-of-staging", Tool: domain.ToolImplement, Repository: cancelRepo, Issue: cancelIssue, Client: "fuseone-staging",
		State: domain.StateRunning, StartedAt: utcNow(), Supersedes: pilotRun}
	if err := fx.runs.Put(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	addr, logs := scopedServer(t, fx)

	answer := callAs(t, addr, stagedBearer, "forja.status", map[string]any{"run_id": "run-of-staging"})
	if !strings.Contains(answer, "run-of-staging") || strings.Contains(answer, pilotRun) || strings.Contains(answer, "supersedes") {
		t.Fatalf("fuseone-staging got %s\nwant its run without supersedes nor %s", answer, pilotRun)
	}
	if logged := logs.String(); !strings.Contains(logged, "client=fuseone-staging") || strings.Contains(logged, pilotRun) {
		t.Fatalf("operator log %q: want the call logged without %s", logged, pilotRun)
	}
}
