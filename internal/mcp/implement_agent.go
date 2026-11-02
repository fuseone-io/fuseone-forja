package mcp

import (
	"context"
	"errors"

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
// under the contract's wall clock. Nothing the agent's tree holds is
// read: the contract and the context are the base commit's.
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
	runCtx, cancel := context.WithTimeout(ctx, wallClock(parsed))
	defer cancel()
	return h.deps.Agent.Run(runCtx, agent.Task{
		RunID: c.RunID, Contract: parsed, Tree: co.Dir(), Issue: issue, Context: agentFiles(files),
	})
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
