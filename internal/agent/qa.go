package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// Candidate is one branch the QA agent may leave an inversion on, and the
// path on the control plane its commits are exported to.
type Candidate struct {
	Branch string
	Bundle string
}

// QATask is one QA agent run on the pull request's head. The contract
// comes from the base commit; the tree is the head checked out at HeadSHA;
// Issue and PullRequestBody are untrusted text that reaches only the
// prompt. Production lists the only paths an inversion may touch, and
// Candidates the branches the agent may leave one on, in the order of
// preference it is asked for.
type QATask struct {
	RunID           string
	Contract        domain.Contract
	Tree            string
	Issue           domain.Issue
	PullRequestBody string
	BaseSHA         string
	HeadSHA         string
	Production      []string
	Candidates      []Candidate
}

// QAResult is how the QA agent run ended and what the proxies recorded for
// it, as a Result is for an implement run. Exported has one entry per
// candidate, in the task's order: whether its commits are in its bundle.
// A capped run exports none.
type QAResult struct {
	ExitCode int
	TimedOut bool
	Capped   bool
	Exported []bool
	Tokens   domain.TokenUsage
	Egress   domain.EgressReport
	Compute  time.Duration
}

// RunQA runs the QA agent as Run runs the implement agent, with the QA
// prompt, and once it exited 0 on its own exports each candidate's
// commits beyond the head to its bundle while the workspace still runs. A
// candidate left at the head or never made is not exported, which is a
// result.
func (r *Runner) RunQA(ctx context.Context, t QATask) (QAResult, error) {
	exported := make([]bool, len(t.Candidates))
	res, err := r.session(ctx, launch{runID: t.RunID, image: t.Contract.Image, tree: t.Tree, prompt: RenderQA(t)},
		func(ctx context.Context, ws Workspace, res *Result) error { return exportCandidates(ctx, ws, t, exported, res) })
	if res.Capped {
		clear(exported)
	}
	return QAResult{
		ExitCode: res.ExitCode, TimedOut: res.TimedOut, Capped: res.Capped, Exported: exported,
		Tokens: res.Tokens, Egress: res.Egress, Compute: res.Compute,
	}, err
}

// exportCandidates exports each candidate in order, marking the ones that
// came out. The deadline ending an export ends them all as a result.
func exportCandidates(ctx context.Context, ws Workspace, t QATask, exported []bool, res *Result) error {
	for i, c := range t.Candidates {
		err := ws.ExportCommits(ctx, t.HeadSHA, c.Branch, c.Bundle)
		switch {
		case err == nil:
			exported[i] = true
		case errors.Is(err, domain.ErrNoCommits), errors.Is(err, domain.ErrNoBranch):
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			res.TimedOut = true
			return nil
		default:
			return fmt.Errorf("agent: export %s: %w", c.Branch, err)
		}
	}
	return nil
}
