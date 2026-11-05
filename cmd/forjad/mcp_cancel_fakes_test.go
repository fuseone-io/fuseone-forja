package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/agent"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/github"
	"github.com/fuseone-io/fuseone-forja/internal/mcp"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

const (
	cancelRepo    = "acme/payments"
	cancelIssue   = 7
	cancelRunID   = "run-cancel-1"
	cancelBaseSHA = "1111111111111111111111111111111111111111"
	cancelBlobSHA = "2222222222222222222222222222222222222222"
)

var cancelContract = []byte("version: 1\nimage: golang:1.25\ngates:\n  - name: build\n    run: make build\n")

// errNotReached is what a dependency no run in these tests reaches answers.
var errNotReached = errors.New("fake: not reached in this test")

// cancelFixture is forja.implement wired with fakes behind the interfaces
// internal/mcp and internal/agent declare, and the real agent runner on a
// workspace whose agent runs until its context ends.
type cancelFixture struct {
	runs   *runstore.Memory
	claims *heldClaims
	ws     *agentUntilCancelled
	deps   mcp.Deps
}

func newCancelFixture(t *testing.T) *cancelFixture {
	t.Helper()
	fx := &cancelFixture{
		runs:   runstore.NewMemory(),
		claims: &heldClaims{held: map[string]domain.Claim{}, released: make(chan struct{})},
		ws:     &agentUntilCancelled{entered: make(chan struct{}), closed: make(chan struct{})},
	}
	co := &baseCheckout{dir: t.TempDir()}
	fx.deps = mcp.Deps{
		Runs: liveRuns{fx.runs}, Contracts: contractAtBase{}, Claims: fx.claims,
		Cloner: baseCloner{co: co}, BranchCloner: unavailablePlan{err: errNotReached},
		Issues: issueReader{}, Gates: refusingGates{},
		Agent:  &agent.Runner{Launcher: wsLauncher{ws: fx.ws}, Now: time.Now},
		NewID:  func() string { return cancelRunID }, Now: utcNow, Version: "test",
	}
	withoutDelivery(&fx.deps, errNotReached)
	return fx
}

// liveRuns is the run store as a networked one behaves: a write on a
// context that has ended fails.
type liveRuns struct {
	*runstore.Memory
}

func (r liveRuns) Put(ctx context.Context, run domain.Run) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("fake: put run: %w", err)
	}
	return r.Memory.Put(ctx, run)
}

// heldClaims holds an issue for one run at a time, as the claim ref does:
// a second claim on a held issue is refused, releasing a claim not held
// is an error, and neither reaches GitHub on a context that has ended.
type heldClaims struct {
	mu       sync.Mutex
	held     map[string]domain.Claim
	released chan struct{}
}

func claimKey(repo string, issue int) string { return repo + "#" + strconv.Itoa(issue) }

func (h *heldClaims) Claim(ctx context.Context, req domain.ClaimRequest) (domain.Claim, error) {
	if err := ctx.Err(); err != nil {
		return domain.Claim{}, fmt.Errorf("fake: claim: %w", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.held[claimKey(req.Repository, req.Issue)]; ok {
		return domain.Claim{}, &domain.ClaimHeldError{Holder: c}
	}
	now := utcNow()
	c := domain.Claim{RunID: req.RunID, Repository: req.Repository, Issue: req.Issue, SHA: cancelBaseSHA, CreatedAt: now, ExpiresAt: now.Add(req.Lease)}
	h.held[claimKey(req.Repository, req.Issue)] = c
	return c, nil
}

func (h *heldClaims) Release(ctx context.Context, c domain.Claim) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("fake: release: %w", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	key := claimKey(c.Repository, c.Issue)
	if held, ok := h.held[key]; !ok || held.RunID != c.RunID {
		return fmt.Errorf("fake: release %s by %s: not held by it", key, c.RunID)
	}
	delete(h.held, key)
	close(h.released)
	return nil
}

func (h *heldClaims) isHeld() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.held) > 0
}

// agentUntilCancelled is an agent workspace whose agent runs until its
// context ends: entered closes when the agent starts, closed when the
// workspace is destroyed.
type agentUntilCancelled struct {
	enterOnce, closeOnce sync.Once
	entered, closed      chan struct{}
}

func (w *agentUntilCancelled) Exec(ctx context.Context, _ string) (agent.ExecResult, error) {
	w.enterOnce.Do(func() { close(w.entered) })
	<-ctx.Done()
	return agent.ExecResult{}, ctx.Err()
}

func (w *agentUntilCancelled) AwaitCap(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (w *agentUntilCancelled) WriteFile(context.Context, string, []byte) error { return nil }

func (w *agentUntilCancelled) Model(context.Context) (agent.ModelReport, error) {
	return agent.ModelReport{}, nil
}

func (w *agentUntilCancelled) Egress(context.Context) (domain.EgressReport, error) {
	return domain.EgressReport{}, nil
}

func (w *agentUntilCancelled) ExportCommits(context.Context, string, string, string) error {
	return errNotReached
}

func (w *agentUntilCancelled) Halt(context.Context) error { return nil }

// Close, as the driver's, cannot reach the daemon on a context that has
// ended, and leaves the workspace running.
func (w *agentUntilCancelled) Close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("fake: close workspace: %w", err)
	}
	w.closeOnce.Do(func() { close(w.closed) })
	return nil
}

func (w *agentUntilCancelled) isClosed() bool {
	select {
	case <-w.closed:
		return true
	default:
		return false
	}
}

type wsLauncher struct {
	ws *agentUntilCancelled
}

func (l wsLauncher) LaunchAgent(context.Context, agent.Launch) (agent.Workspace, error) {
	return l.ws, nil
}

// contractAtBase is the base commit's contract the lease is sized from.
type contractAtBase struct{}

func (contractAtBase) Read(context.Context, string) (domain.ContractSource, error) {
	return domain.ContractSource{SHA: cancelBaseSHA, Body: cancelContract}, nil
}

type issueReader struct{}

func (issueReader) Read(_ context.Context, _ string, n int) (domain.Issue, error) {
	return domain.Issue{Number: n, Title: "an issue"}, nil
}

type refusingGates struct{}

func (refusingGates) Run(context.Context, string, domain.Contract, string) (domain.GateReport, error) {
	return domain.GateReport{}, errNotReached
}

type baseCloner struct {
	co *baseCheckout
}

func (c baseCloner) Clone(context.Context, string, string) (mcp.Checkout, error) { return c.co, nil }

// baseCheckout is the base commit holding the contract and nothing else.
type baseCheckout struct {
	dir string
}

func (c *baseCheckout) SHA() string    { return cancelBaseSHA }
func (c *baseCheckout) Start() string  { return cancelBaseSHA }
func (c *baseCheckout) Branch() string { return "main" }
func (c *baseCheckout) Dir() string    { return c.dir }
func (c *baseCheckout) Close() error   { return nil }

func (c *baseCheckout) ReadFile(_ context.Context, path string) ([]byte, error) {
	if path == ".forja.yaml" {
		return cancelContract, nil
	}
	return nil, fmt.Errorf("%s: %w at %s", path, github.ErrPathMissing, cancelBaseSHA)
}

func (c *baseCheckout) BlobSHA(_ context.Context, path string) (string, error) {
	if path == ".forja.yaml" {
		return cancelBlobSHA, nil
	}
	return "", fmt.Errorf("%s: %w at %s", path, github.ErrPathMissing, cancelBaseSHA)
}

func (c *baseCheckout) ListFiles(_ context.Context, dir string) ([]string, error) {
	return nil, fmt.Errorf("%s: %w at %s", dir, github.ErrPathMissing, cancelBaseSHA)
}

func (c *baseCheckout) StartBranch(context.Context, string) error { return nil }

func (c *baseCheckout) Adopt(context.Context, string, string) (string, error) {
	return "", errNotReached
}

func (c *baseCheckout) DiffLines(context.Context, string) (int, error) { return 0, errNotReached }

func (c *baseCheckout) Diff(context.Context, string) ([]domain.FileChange, error) {
	return nil, errNotReached
}

func (c *baseCheckout) Changes(context.Context, string, string) (domain.Change, error) {
	return domain.Change{}, errNotReached
}
