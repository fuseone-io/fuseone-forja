package mcp

import (
	"context"
	"errors"
	"fmt"
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

// finishTimeout bounds the release and the terminal record once the caller
// is gone: the ref must come off and the run must end whether or not
// anyone is still waiting for the answer.
const finishTimeout = 30 * time.Second

type implementer struct {
	deps Deps
}

// implementHandler reserves the issue, records the run, runs the agent and
// releases the reservation. A refused claim is a tool error naming the
// holder and records no run.
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
	lease, err := h.lease(ctx, in.Repository)
	if err != nil {
		return ImplementOutput{}, err
	}
	c, err := h.deps.Claims.Claim(ctx, domain.ClaimRequest{
		Repository: in.Repository, Issue: in.Issue, RunID: h.deps.NewID(), Lease: lease,
	})
	if err != nil {
		return ImplementOutput{}, err
	}
	if err := h.run(ctx, c, in.BaseBranch); err != nil {
		return ImplementOutput{}, fmt.Errorf("run %s: %w", c.RunID, err)
	}
	return ImplementOutput{RunID: c.RunID}, nil
}

func (h implementer) wired() error {
	d := h.deps
	if d.Runs == nil || d.Contracts == nil || d.Claims == nil || d.Cloner == nil || d.Issues == nil || d.NewID == nil || d.Now == nil {
		return errors.New("not wired: runs, contracts, claims, cloner, issues, NewID and Now are all required")
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

// run records the run as running, runs the agent on the base branch,
// releases the claim and records the terminal state with what the
// proxies recorded, on every outcome. Any failure of the work or the
// release is returned after the run was recorded failed.
func (h implementer) run(ctx context.Context, c domain.Claim, base string) error {
	run := domain.Run{
		ID: c.RunID, Tool: domain.ToolImplement, Repository: c.Repository, Issue: c.Issue,
		State: domain.StateRunning, StartedAt: h.deps.Now(),
	}
	if c.Supersedes != nil {
		run.Supersedes = c.Supersedes.RunID
	}
	// The caller may be gone by now. Once the issue is claimed, the records
	// and the release run on a context detached from the caller's, with a
	// deadline of their own, so neither the ref nor a run stuck in running
	// is left behind.
	done, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	if err := h.deps.Runs.Put(done, run); err != nil {
		return errors.Join(fmt.Errorf("record run: %w", err), h.release(done, c))
	}
	res, workErr := h.work(ctx, c, base)
	releaseErr := h.release(done, c)
	finished := h.deps.Now()
	run.FinishedAt = &finished
	run.Tokens, run.Egress = res.Tokens, res.Egress
	run.State, run.Reason = outcome(res, workErr, releaseErr)
	if err := h.deps.Runs.Put(done, run); err != nil {
		return errors.Join(workErr, releaseErr, fmt.Errorf("record run: %w", err))
	}
	return errors.Join(workErr, releaseErr)
}

func (h implementer) release(ctx context.Context, c domain.Claim) error {
	if err := h.deps.Claims.Release(ctx, c); err != nil {
		return fmt.Errorf("release claim: %w", err)
	}
	return nil
}
