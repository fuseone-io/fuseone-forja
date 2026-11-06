package mcp

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/agent"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

// boundedRuns is the in-memory store counting every record made, after
// the caller left, on a context that is still live and bounded: one
// FinishTimeout step each.
type boundedRuns struct {
	*runstore.Memory
	gone func() bool

	mu      sync.Mutex
	bounded int
}

func (r *boundedRuns) Put(ctx context.Context, run domain.Run) error {
	r.mu.Lock()
	if _, ok := ctx.Deadline(); ok && ctx.Err() == nil && r.gone() {
		r.bounded++
	}
	r.mu.Unlock()
	return r.Memory.Put(ctx, run)
}

func (r *boundedRuns) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bounded
}

// deadCloner refuses a context that ended, as the real cloner's first
// request does.
type deadCloner struct{ co Checkout }

func (c deadCloner) Clone(ctx context.Context, _, _ string) (Checkout, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.co, nil
}

func (c deadCloner) CloneBranch(ctx context.Context, repo, base, _ string) (Checkout, error) {
	return c.Clone(ctx, repo, base)
}

// grantingClaims grants every claim and counts the releases.
type grantingClaims struct {
	now time.Time

	mu       sync.Mutex
	releases int
}

func (c *grantingClaims) Claim(_ context.Context, req domain.ClaimRequest) (domain.Claim, error) {
	return domain.Claim{RunID: req.RunID, Repository: req.Repository, Issue: req.Issue, SHA: "sha", CreatedAt: c.now, ExpiresAt: c.now.Add(req.Lease)}, nil
}

func (c *grantingClaims) Release(context.Context, domain.Claim) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.releases++
	return nil
}

// leavingAgent is the agent the caller leaves during: it fails with the
// cancellation, as the real runner does once its own teardown is over.
type leavingAgent struct{ leave context.CancelFunc }

func (a leavingAgent) Run(ctx context.Context, _ agent.Task) (agent.Result, error) {
	a.leave()
	<-ctx.Done()
	return agent.Result{ExitCode: domain.NoExitCode}, ctx.Err()
}

// leavingGates are the gates the caller leaves during, which still come
// back green: the runner read their logs and removed the workspace on
// contexts of its own.
type leavingGates struct{ leave context.CancelFunc }

func (g leavingGates) Run(context.Context, string, domain.Contract, string) (domain.GateReport, error) {
	g.leave()
	return domain.GateReport{}, nil
}

// cancelPoint wires the dependency during which the caller leaves.
type cancelPoint func(deps *Deps, leave context.CancelFunc)

func cancelPoints() map[string]cancelPoint {
	return map[string]cancelPoint{
		"during the claim": func(d *Deps, leave context.CancelFunc) {
			d.Claims = &cancellingClaims{cancel: leave, now: d.Now()}
		},
		"during the agent": func(d *Deps, leave context.CancelFunc) { d.Agent = leavingAgent{leave: leave} },
		"during the gates": func(d *Deps, leave context.CancelFunc) { d.Gates = leavingGates{leave: leave} },
	}
}

// releasesOf is how many releases the claimer in deps was asked for.
func releasesOf(t *testing.T, claims Claimer) int {
	t.Helper()
	switch c := claims.(type) {
	case *cancellingClaims:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.releases
	case *grantingClaims:
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.releases
	}
	t.Fatalf("unknown claimer %T", claims)
	return 0
}

// Wherever the caller leaves, the run takes at most CancelledRunRecords
// FinishTimeout-bounded steps after the call it left during: no further
// stage starts, only the record ending what was in flight and the release.
// The server's drain is sized from that count; a step added after the
// caller left fails here first.
func TestImplement_callerGone_boundedStepsAfterTheCallInFlightAtMostCancelledRunRecords(t *testing.T) {
	t.Parallel()
	for name, leaveAt := range cancelPoints() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
			runs := &boundedRuns{Memory: runstore.NewMemory(), gone: func() bool { return ctx.Err() != nil }}
			body := []byte("version: 1\nimage: golang:1.25\ngates:\n  - name: build\n    run: make build\nlimits:\n  wall_clock: 30m\n")
			cloner := deadCloner{co: oneFileCheckout{contract: body}}
			deps := Deps{
				Runs: runs, Claims: &grantingClaims{now: now}, Contracts: fixedContracts{body: body},
				Cloner: cloner, BranchCloner: cloner, Issues: staticIssues{}, Identity: acceptingGitHub{},
				Agent: exitingAgent{}, Gates: greenGates{},
				Pusher: acceptingGitHub{}, PullRequests: acceptingGitHub{}, Statuses: acceptingGitHub{},
				NewID: func() string { return "run-cancelled" }, Now: func() time.Time { return now },
			}
			leaveAt(&deps, cancel)

			_, _, _ = implementHandler(deps)(withClient(ctx, domain.Client{Name: "test-client", Unrestricted: true}), nil, ImplementInput{Repository: "fuseone-io/fuseone-forja", Issue: 12})
			if ctx.Err() == nil {
				t.Fatal("the caller never left: the test checks nothing")
			}
			releases := releasesOf(t, deps.Claims)
			if got := runs.count() + releases; got > CancelledRunRecords || releases != 1 {
				t.Fatalf("bounded steps after the caller left = %d records + %d releases = %d, want one release and at most CancelledRunRecords = %d",
					runs.count(), releases, got, CancelledRunRecords)
			}
		})
	}
}
