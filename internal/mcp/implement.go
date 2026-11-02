package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/contract"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// ContractReader is what forja.implement needs from GitHub: the contract
// at the commit of the base branch, with that commit's SHA.
type ContractReader interface {
	Read(ctx context.Context, repo string) (domain.ContractSource, error)
}

// Claimer is what forja.implement needs to hold an issue for one run.
type Claimer interface {
	Claim(ctx context.Context, req domain.ClaimRequest) (domain.Claim, error)
	Release(ctx context.Context, c domain.Claim) error
}

// RunStore is what forja.implement writes and forja.status reads.
type RunStore interface {
	RunReader
	Put(ctx context.Context, run domain.Run) error
}

// finishTimeout bounds each record of the run and the release, counted
// from when each starts: the ref must come off and the run must end
// whether or not anyone is still waiting for the answer.
const finishTimeout = 30 * time.Second

type implementer struct {
	deps Deps
}

// implementHandler reserves the issue, records the run, runs the agent,
// gates its head and delivers it, and releases the reservation. A refused
// claim, or a correction round on a branch Forja did not open, is a tool
// error and records no run; a run that failed on its own terms, such as a
// red gate, is a result.
func implementHandler(deps Deps) sdk.ToolHandlerFor[ImplementInput, ImplementOutput] {
	h := implementer{deps: deps}
	return func(ctx context.Context, _ *sdk.CallToolRequest, in ImplementInput) (*sdk.CallToolResult, ImplementOutput, error) {
		out, err := h.implement(ctx, in)
		if err != nil {
			return nil, ImplementOutput{}, fmt.Errorf("%s: %w", domain.ToolImplement, err)
		}
		return nil, out, nil
	}
}

func (h implementer) implement(ctx context.Context, in ImplementInput) (ImplementOutput, error) {
	if err := h.wired(); err != nil {
		return ImplementOutput{}, err
	}
	if in.Issue <= 0 {
		return ImplementOutput{}, fmt.Errorf("issue %d is not positive", in.Issue)
	}
	rd, err := h.openRound(ctx, in)
	if err != nil {
		return ImplementOutput{}, err
	}
	lease, err := h.lease(ctx, in.Repository)
	if err != nil {
		return ImplementOutput{}, err
	}
	started := h.deps.Now()
	c, err := h.deps.Claims.Claim(ctx, domain.ClaimRequest{
		Repository: in.Repository, Issue: in.Issue, RunID: h.deps.NewID(), Lease: lease,
	})
	if err != nil {
		return ImplementOutput{}, err
	}
	f := newFlight(h.deps, c, in.BaseBranch, claimStage(started, h.deps.Now()))
	f.withRound(rd)
	if err := h.run(ctx, f); err != nil {
		return ImplementOutput{}, fmt.Errorf("run %s: %w", c.RunID, err)
	}
	return implementOutput(f.run, f.evidence), nil
}

// wired refuses a server missing any dependency a run reaches, naming the
// missing ones; a missing agent names the variables that configure one.
func (h implementer) wired() error {
	d := h.deps
	required := []struct {
		name string
		set  bool
	}{
		{"runs", d.Runs != nil}, {"contracts", d.Contracts != nil}, {"claims", d.Claims != nil},
		{"cloner", d.Cloner != nil}, {"issues", d.Issues != nil}, {"gates", d.Gates != nil},
		{"pusher", d.Pusher != nil}, {"pull requests", d.PullRequests != nil}, {"statuses", d.Statuses != nil},
		{"branch cloner", d.BranchCloner != nil}, {"identity", d.Identity != nil},
		{"NewID", d.NewID != nil}, {"Now", d.Now != nil},
	}
	var missing []string
	for _, r := range required {
		if !r.set {
			missing = append(missing, r.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("not wired: %s required", strings.Join(missing, ", "))
	}
	if d.Agent == nil {
		return errNoAgent
	}
	return nil
}

// lease reads the contract at the base commit and sizes the lease from its
// limits; a contract that does not parse reserves nothing.
func (h implementer) lease(ctx context.Context, repo string) (time.Duration, error) {
	src, err := h.deps.Contracts.Read(ctx, repo)
	if err != nil {
		return 0, err
	}
	parsed, err := contract.Parse(src.Body)
	if err != nil {
		return 0, fmt.Errorf("contract at %s: %w", src.SHA, err)
	}
	return domain.LeaseFor(parsed.Limits, h.deps.Lease)
}

// run records the run as running, works it stage by stage, releases the
// claim and records the terminal state, on every outcome. It is succeeded
// only when every stage and the release worked. Any failure of the work
// or the release is returned after the run was recorded failed.
func (h implementer) run(ctx context.Context, f *flight) error {
	// The caller may be gone by now. Once the issue is claimed, the records
	// and the release run on a context detached from the caller's, each
	// bounded from its own start, so neither the ref nor a run stuck in
	// running is left behind however long the work took.
	done := context.WithoutCancel(ctx)
	if err := f.put(done); err != nil {
		return errors.Join(err, h.release(done, f.claim))
	}
	delivered, workErr := h.work(ctx, done, f)
	releaseErr := h.releaseStage(done, f)
	finished := h.deps.Now()
	f.run.FinishedAt, f.run.State = &finished, domain.StateFailed
	if delivered && workErr == nil && releaseErr == nil {
		f.run.State, f.run.Reason = domain.StateSucceeded, ""
	}
	if err := f.put(done); err != nil {
		return errors.Join(workErr, releaseErr, err)
	}
	return errors.Join(workErr, releaseErr)
}

// releaseStage frees the issue as the release stage. The release happens
// even when its start could not be recorded; the terminal record ends it.
func (h implementer) releaseStage(done context.Context, f *flight) error {
	recordErr := f.begin(done, domain.StageRelease)
	err := h.release(done, f.claim)
	f.finish(err == nil)
	return errors.Join(err, recordErr)
}

func (h implementer) release(ctx context.Context, c domain.Claim) error {
	ctx, cancel := context.WithTimeout(ctx, finishTimeout)
	defer cancel()
	if err := h.deps.Claims.Release(ctx, c); err != nil {
		return fmt.Errorf("release claim: %w", err)
	}
	return nil
}
