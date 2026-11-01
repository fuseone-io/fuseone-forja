package mcp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

type fixedContracts struct{ body []byte }

func (f fixedContracts) Read(context.Context, string) (domain.ContractSource, error) {
	return domain.ContractSource{SHA: "3d5656a3e223a7bc54b8e8f13086f562b84dc90c", Body: f.body}, nil
}

// cancellingClaims cancels the caller's context the moment the claim is
// granted and records the context the release then arrived with.
type cancellingClaims struct {
	cancel context.CancelFunc

	mu            sync.Mutex
	releases      int
	releaseCtxErr error
	releaseDline  bool
}

func (c *cancellingClaims) Claim(_ context.Context, req domain.ClaimRequest) (domain.Claim, error) {
	c.cancel()
	return domain.Claim{RunID: req.RunID, Repository: req.Repository, Issue: req.Issue, SHA: "sha"}, nil
}

func (c *cancellingClaims) Release(ctx context.Context, _ domain.Claim) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.releases++
	c.releaseCtxErr = ctx.Err()
	_, c.releaseDline = ctx.Deadline()
	return nil
}

// The caller going away between the claim and the release must not leave
// the ref behind: the release runs on a context that outlives the call,
// bounded by its own deadline.
func TestImplement_callerCancelled_releaseStillHappens(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	claims := &cancellingClaims{cancel: cancel}
	runs := runstore.NewMemory()
	deps := Deps{
		Runs: runs, Claims: claims,
		Contracts: fixedContracts{body: []byte("version: 1\nimage: golang:1.25\ngates:\n  - name: build\n    run: make build\n")},
		NewID:     func() string { return "run-cancelled" },
		Now:       func() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) },
	}

	_, out, err := implementHandler(deps)(ctx, nil, ImplementInput{Repository: "fuseone-io/fuseone-forja", Issue: 12})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("the fixture did not cancel the caller's context")
	}
	claims.mu.Lock()
	defer claims.mu.Unlock()
	if claims.releases != 1 {
		t.Fatalf("releases = %d, want 1", claims.releases)
	}
	if claims.releaseCtxErr != nil {
		t.Fatalf("release ran on a context already done: %v", claims.releaseCtxErr)
	}
	if !claims.releaseDline {
		t.Fatal("release ran with no deadline of its own")
	}
	run, err := runs.Get(context.Background(), out.RunID)
	if err != nil || run.State != domain.StateSucceeded {
		t.Fatalf("run = %+v, %v; want succeeded", run, err)
	}
}
