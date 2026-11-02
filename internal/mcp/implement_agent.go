package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/agent"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// AgentRunner is what forja.implement needs to run the agent on a
// checkout: the agent's outcome and what the proxies recorded.
type AgentRunner interface {
	Run(ctx context.Context, t agent.Task) (agent.Result, error)
}

// errNoAgent names what the operator sets to give forja.implement an
// agent.
var errNoAgent = errors.New("not wired: no agent; set FORJA_MODEL_API_KEY, FORJA_MODEL_TOKEN_CAP and FORJA_AGENT_BINARY")

// work clones base, reads the contract and the agent_context at that
// commit and the issue from GitHub, and runs the agent on the checkout
// under the contract's wall clock, which never outlives the claim's lease.
// Nothing the agent's tree holds is read: the contract and the context are
// the base commit's.
func (h implementer) work(ctx context.Context, c domain.Claim, base string) (agent.Result, error) {
	none := agent.Result{ExitCode: domain.NoExitCode}
	co, err := h.deps.Cloner.Clone(ctx, c.Repository, base)
	if err != nil {
		return none, err
	}
	// The run's outcome is the agent's; a clone directory that could not be
	// removed changes none of it.
	defer func() { _ = co.Close() }()
	parsed, _, err := readContract(ctx, co, c.Repository)
	if err != nil {
		return none, err
	}
	issue, err := h.deps.Issues.Read(ctx, c.Repository, c.Issue)
	if err != nil {
		return none, err
	}
	files, _, err := agentContext(ctx, co, parsed.AgentContext)
	if err != nil {
		return none, err
	}
	left, err := h.timeLeft(c, parsed)
	if err != nil {
		return none, fmt.Errorf("contract at %s: %w", co.SHA(), err)
	}
	runCtx, cancel := context.WithTimeout(ctx, left)
	defer cancel()
	return h.deps.Agent.Run(runCtx, agent.Task{
		RunID: c.RunID, Contract: parsed, Tree: co.Dir(), Issue: issue, Context: agentFiles(files),
	})
}

// timeLeft is how long the agent may run under the held claim: the
// contract's wall clock counted from the claim. The contract the agent runs
// under may differ from the one the lease was sized from, so it is refused
// when it asks for a longer lease than the claim holds, or when its wall
// clock would end at or after the lease, or is already spent.
func (h implementer) timeLeft(c domain.Claim, parsed domain.Contract) (time.Duration, error) {
	held := c.ExpiresAt.Sub(c.CreatedAt)
	need, err := domain.LeaseFor(parsed.Limits, h.deps.Lease)
	if err != nil {
		return 0, err
	}
	if need > held {
		return 0, fmt.Errorf("needs a lease of %v, the claim holds %v", need, held)
	}
	end := c.CreatedAt.Add(wallClock(parsed))
	if !end.Before(c.ExpiresAt) {
		return 0, fmt.Errorf("wall clock %v does not end before the claim's lease of %v", wallClock(parsed), held)
	}
	left := end.Sub(h.deps.Now())
	if left <= 0 {
		return 0, fmt.Errorf("wall clock %v spent before the agent started, within the claim's lease of %v", wallClock(parsed), held)
	}
	return left, nil
}

func agentFiles(files []ContextFile) []agent.File {
	out := make([]agent.File, 0, len(files))
	for _, f := range files {
		out = append(out, agent.File{Path: f.Path, Content: f.Content})
	}
	return out
}

// outcome is the terminal state of an implement run. The cap is a reason
// whatever else happened; success needs the agent to have exited 0 on its
// own and every step around it to have worked.
func outcome(res agent.Result, workErr, releaseErr error) (domain.RunState, domain.FailureReason) {
	switch {
	case res.Capped:
		return domain.StateFailed, domain.ReasonTokenCap
	case workErr != nil || releaseErr != nil || res.TimedOut || res.ExitCode != 0:
		return domain.StateFailed, ""
	}
	return domain.StateSucceeded, ""
}
