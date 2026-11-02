package mcp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/agent"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/github"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

type fixedContracts struct{ body []byte }

func (f fixedContracts) Read(context.Context, string) (domain.ContractSource, error) {
	return domain.ContractSource{SHA: "3d5656a3e223a7bc54b8e8f13086f562b84dc90c", Body: f.body}, nil
}

// cancellingClaims cancels the caller's context the moment the claim is
// granted, for the requested lease from now, and records the context the
// release then arrived with.
type cancellingClaims struct {
	cancel context.CancelFunc
	now    time.Time

	mu            sync.Mutex
	releases      int
	releaseCtxErr error
	releaseDline  bool
}

func (c *cancellingClaims) Claim(_ context.Context, req domain.ClaimRequest) (domain.Claim, error) {
	c.cancel()
	return domain.Claim{
		RunID: req.RunID, Repository: req.Repository, Issue: req.Issue, SHA: "sha",
		CreatedAt: c.now, ExpiresAt: c.now.Add(req.Lease),
	}, nil
}

func (c *cancellingClaims) Release(ctx context.Context, _ domain.Claim) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.releases++
	c.releaseCtxErr = ctx.Err()
	_, c.releaseDline = ctx.Deadline()
	return nil
}

// oneFileCheckout holds only the contract.
type oneFileCheckout struct{ contract []byte }

func (oneFileCheckout) SHA() string    { return "3d5656a3e223a7bc54b8e8f13086f562b84dc90c" }
func (oneFileCheckout) Start() string  { return "3d5656a3e223a7bc54b8e8f13086f562b84dc90c" }
func (oneFileCheckout) Branch() string { return "main" }
func (oneFileCheckout) Dir() string    { return "/tmp/forja-plan-internal" }
func (oneFileCheckout) Close() error   { return nil }
func (c oneFileCheckout) ReadFile(_ context.Context, path string) ([]byte, error) {
	if path != ".forja.yaml" {
		return nil, github.ErrPathMissing
	}
	return c.contract, nil
}
func (oneFileCheckout) ListFiles(context.Context, string) ([]string, error) { return nil, nil }
func (oneFileCheckout) BlobSHA(context.Context, string) (string, error)     { return "blob", nil }
func (oneFileCheckout) StartBranch(context.Context, string) error           { return nil }
func (oneFileCheckout) Adopt(context.Context, string, string) (string, error) {
	return "04915d552e6e507929c4c734b4f4a3a31ae05820", nil
}

// greenGates passes every step.
type greenGates struct{}

func (greenGates) Run(context.Context, string, domain.Contract, string) (domain.GateReport, error) {
	return domain.GateReport{}, nil
}

// acceptingGitHub takes every push, pull request and status.
type acceptingGitHub struct{}

func (acceptingGitHub) Push(context.Context, github.PushRequest) error { return nil }
func (acceptingGitHub) Open(context.Context, github.PullRequestRequest) (domain.PullRequest, error) {
	return domain.PullRequest{Number: 1, URL: "https://github.com/fuseone-io/fuseone-forja/pull/1"}, nil
}
func (acceptingGitHub) Publish(context.Context, github.StatusRequest) error { return nil }
func (acceptingGitHub) Find(context.Context, string, string) (domain.PullRequest, error) {
	return domain.PullRequest{}, github.ErrNoPullRequest
}
func (acceptingGitHub) Comment(context.Context, github.CommentRequest) error { return nil }
func (acceptingGitHub) Login(context.Context) (string, error)                { return "forja[bot]", nil }

type checkoutCloner struct{ co Checkout }

func (c checkoutCloner) Clone(context.Context, string, string) (Checkout, error) { return c.co, nil }
func (c checkoutCloner) CloneBranch(context.Context, string, string, string) (Checkout, error) {
	return c.co, nil
}

type staticIssues struct{}

func (staticIssues) Read(context.Context, string, int) (domain.Issue, error) {
	return domain.Issue{Number: 12, Title: "t", Body: "b"}, nil
}

// exitingAgent exits 0 with a commit whatever its context says, as an
// agent that had already finished would.
type exitingAgent struct{}

func (exitingAgent) Run(context.Context, agent.Task) (agent.Result, error) {
	return agent.Result{Exported: true}, nil
}

// The caller going away between the claim and the release must not leave
// the ref behind: the release runs on a context that outlives the call,
// bounded by its own deadline.
func TestImplement_callerCancelled_releaseStillHappens(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	claims := &cancellingClaims{cancel: cancel, now: now}
	runs := runstore.NewMemory()
	body := []byte("version: 1\nimage: golang:1.25\ngates:\n  - name: build\n    run: make build\n")
	deps := Deps{
		Runs: runs, Claims: claims,
		Contracts:    fixedContracts{body: body},
		Cloner:       checkoutCloner{co: oneFileCheckout{contract: body}},
		BranchCloner: checkoutCloner{co: oneFileCheckout{contract: body}},
		Identity:     acceptingGitHub{},
		Issues:       staticIssues{},
		Agent:        exitingAgent{},
		Gates:        greenGates{},
		Pusher:       acceptingGitHub{}, PullRequests: acceptingGitHub{}, Statuses: acceptingGitHub{},
		NewID: func() string { return "run-cancelled" },
		Now:   func() time.Time { return now },
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
