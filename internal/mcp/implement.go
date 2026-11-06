package mcp

import (
	"context"
	"fmt"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

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

// FinishTimeout bounds each record of the run and the release, counted
// from when each starts: the ref must come off and the run must end
// whether or not anyone is still waiting for the answer.
const FinishTimeout = 30 * time.Second

// CancelledRunRecords is the most FinishTimeout-bounded calls a run makes,
// one after the other, once its caller left: left during the claim, five
// records and the release, recording the run running, the clone stage's
// start and end, the release's start, the release, and the run's end. A
// caller that left later, as the agent ran, leaves fewer, after the
// agent's own teardown.
const CancelledRunRecords = 6

type implementer struct {
	lifecycle
}

// implementHandler reserves the issue, records the run, runs the agent,
// gates its head and delivers it, and releases the reservation. A refused
// claim, or a correction round on a branch Forja did not open, is a tool
// error and records no run; a run that failed on its own terms, such as a
// red gate, is a result.
func implementHandler(deps Deps) sdk.ToolHandlerFor[ImplementInput, ImplementOutput] {
	h := implementer{lifecycle{deps: deps}}
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
	f, err := h.claim(ctx, runTarget{tool: domain.ToolImplement, repo: in.Repository, issue: in.Issue, base: in.BaseBranch})
	if err != nil {
		return ImplementOutput{}, err
	}
	f.withRound(rd)
	if err := h.run(ctx, f, h.work); err != nil {
		return ImplementOutput{}, fmt.Errorf("run %s: %w", f.claim.RunID, err)
	}
	return implementOutput(f.run, f.evidence, h.deps.Prices), nil
}

// wired refuses a server missing any dependency a run reaches, naming the
// missing ones; a missing agent names the variables that configure one.
func (h implementer) wired() error {
	d := h.deps
	err := notWired([]dependency{
		{"runs", d.Runs != nil}, {"contracts", d.Contracts != nil}, {"claims", d.Claims != nil},
		{"cloner", d.Cloner != nil}, {"issues", d.Issues != nil}, {"gates", d.Gates != nil},
		{"pusher", d.Pusher != nil}, {"pull requests", d.PullRequests != nil}, {"statuses", d.Statuses != nil},
		{"branch cloner", d.BranchCloner != nil}, {"identity", d.Identity != nil},
		{"NewID", d.NewID != nil}, {"Now", d.Now != nil},
	})
	if err != nil {
		return err
	}
	if d.Agent == nil {
		return errNoAgent
	}
	return nil
}
