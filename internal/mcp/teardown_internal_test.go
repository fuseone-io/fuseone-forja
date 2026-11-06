package mcp

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

// boundedRuns is the in-memory store counting every record made on a
// context that is still live and bounded: one FinishTimeout step each.
type boundedRuns struct {
	*runstore.Memory

	mu      sync.Mutex
	bounded int
}

func (r *boundedRuns) Put(ctx context.Context, run domain.Run) error {
	r.mu.Lock()
	if _, ok := ctx.Deadline(); ok && ctx.Err() == nil {
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

// A caller gone during the claim leaves the run the most steps to take
// after it: every record and the release then runs, one after the other,
// each to its FinishTimeout. Their count is CancelledRunRecords, which the
// server's drain is sized from; a step added there fails here first.
func TestImplement_callerGoneDuringClaim_boundedStepsAreCancelledRunRecords(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	claims := &cancellingClaims{cancel: cancel, now: now}
	runs := &boundedRuns{Memory: runstore.NewMemory()}
	body := []byte("version: 1\nimage: golang:1.25\ngates:\n  - name: build\n    run: make build\nlimits:\n  wall_clock: 30m\n")
	cloner := deadCloner{co: oneFileCheckout{contract: body}}
	deps := Deps{
		Runs: runs, Claims: claims, Contracts: fixedContracts{body: body},
		Cloner: cloner, BranchCloner: cloner, Issues: staticIssues{}, Identity: acceptingGitHub{},
		Agent: cancelledAgent{}, Gates: greenGates{},
		Pusher: acceptingGitHub{}, PullRequests: acceptingGitHub{}, Statuses: acceptingGitHub{},
		NewID: func() string { return "run-cancelled" }, Now: func() time.Time { return now },
	}

	_, _, _ = implementHandler(deps)(withClient(ctx, domain.Client{Name: "test-client", Unrestricted: true}), nil, ImplementInput{Repository: "fuseone-io/fuseone-forja", Issue: 12})
	claims.mu.Lock()
	releases := claims.releases
	claims.mu.Unlock()
	if got := runs.count() + releases; got != CancelledRunRecords {
		t.Fatalf("bounded steps after the caller left = %d records + %d releases = %d, want CancelledRunRecords = %d",
			runs.count(), releases, got, CancelledRunRecords)
	}
}
