package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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

// branchPrefix is the namespace of the branches runs push.
const branchPrefix = "forja/"

// errWallClock is the cause of a run's deadline: the contract's wall
// clock ran out.
var errWallClock = errors.New("limits.wall_clock spent")

// runStep is one stage after the clone, run under the run's deadline
// with its records on done.
type runStep func(ctx, done context.Context, f *flight) (bool, error)

// work clones the base, then runs the agent, the gates and the delivery
// under one deadline: the contract's wall clock counted from the claim,
// which never outlives the claim's lease. Nothing the agent's tree holds
// is read: the contract and the context are the base commit's. delivered
// says every stage worked.
func (h implementer) work(ctx, done context.Context, f *flight) (delivered bool, err error) {
	defer f.closeCheckout()
	if ok, err := h.cloneStage(ctx, done, f); !ok || err != nil {
		return false, err
	}
	runCtx, cancel := context.WithTimeoutCause(ctx, f.left, errWallClock)
	defer cancel()
	dir, err := os.MkdirTemp("", "forja-bundle-*")
	if err != nil {
		return false, fmt.Errorf("bundle directory: %w", err)
	}
	// The bundle was imported or refused by now; a directory that could not
	// be removed changes neither.
	defer func() { _ = os.RemoveAll(dir) }()
	f.bundle = filepath.Join(dir, "commits.bundle")
	for _, step := range []runStep{h.agentStage, h.gatesStage, h.diffStage, h.pushStage, h.pullRequestStage, h.statusStage} {
		if ok, err := step(runCtx, done, f); !ok || err != nil {
			return false, stopped(runCtx, f, err)
		}
	}
	return true, nil
}

// stopped is what a stage that ended the run leaves to return. When the
// wall clock ran out, the run failed for it, a result whatever the stage
// met on the way: it names the reason unless the cap or the run's own
// terms already named a more specific one than a red gate. Otherwise it
// is the stage's error.
func stopped(runCtx context.Context, f *flight, err error) error {
	if !errors.Is(context.Cause(runCtx), errWallClock) {
		return err
	}
	if f.run.Reason == "" || f.run.Reason == domain.ReasonGateFailed {
		f.run.Reason = domain.ReasonWallClock
	}
	return nil
}

// cloneStage clones the base branch and reads at that commit what the run
// needs, then starts the run's branch.
func (h implementer) cloneStage(ctx, done context.Context, f *flight) (bool, error) {
	return f.stage(done, domain.StageClone, func() (bool, error) {
		co, err := h.deps.Cloner.Clone(ctx, f.run.Repository, f.base)
		if err != nil {
			return false, err
		}
		f.co, f.run.BaseSHA = co, co.SHA()
		return true, h.prepare(ctx, f)
	})
}

// prepare reads the contract, the issue and the agent_context of the base
// checkout, sizes what is left of the wall clock and starts the branch.
func (h implementer) prepare(ctx context.Context, f *flight) error {
	parsed, contractSHA, err := readContract(ctx, f.co, f.run.Repository)
	if err != nil {
		return err
	}
	f.contract, f.run.ContractSHA, f.run.Limits = parsed, contractSHA, parsed.Limits.Effective()
	if f.issue, err = h.deps.Issues.Read(ctx, f.run.Repository, f.run.Issue); err != nil {
		return err
	}
	if f.context, _, err = agentContext(ctx, f.co, parsed.AgentContext); err != nil {
		return err
	}
	if f.left, err = h.timeLeft(f.claim, parsed); err != nil {
		return fmt.Errorf("contract at %s: %w", f.co.SHA(), err)
	}
	f.run.Branch = branchPrefix + strconv.Itoa(f.run.Issue)
	return f.co.StartBranch(ctx, f.run.Branch)
}

// agentStage runs the agent on the checkout and imports its commits. The
// cap is the reason whatever else happened; an agent stopped, failed or
// that committed nothing ends the run without an error.
func (h implementer) agentStage(ctx, done context.Context, f *flight) (bool, error) {
	return f.stage(done, domain.StageAgent, func() (bool, error) {
		res, err := h.deps.Agent.Run(ctx, agent.Task{
			RunID: f.run.ID, Contract: f.contract, Tree: f.co.Dir(), Issue: f.issue, Context: agentFiles(f.context),
			Branch: f.run.Branch, BaseSHA: f.run.BaseSHA, Bundle: f.bundle,
		})
		f.run.Tokens, f.run.Egress = res.Tokens, res.Egress
		if res.Capped {
			f.run.Reason = domain.ReasonTokenCap
		}
		switch {
		case err != nil:
			return false, err
		case res.Capped || res.TimedOut || res.ExitCode != 0:
			return false, nil
		case !res.Exported:
			f.run.Reason = domain.ReasonNoCommits
			return false, nil
		}
		return h.adopt(ctx, f)
	})
}

// adopt brings the exported commits into the checkout and counts what the
// head changes against the base on the control plane's own clone; a head
// at the base is no commit at all.
func (h implementer) adopt(ctx context.Context, f *flight) (bool, error) {
	head, err := f.co.Adopt(ctx, f.bundle, f.run.Branch)
	if errors.Is(err, domain.ErrNoCommits) {
		f.run.Reason = domain.ReasonNoCommits
		return false, nil
	}
	if err != nil {
		return false, err
	}
	f.run.HeadSHA = head
	lines, err := f.co.DiffLines(ctx, head)
	if err != nil {
		return false, err
	}
	f.run.DiffLines = &lines
	return true, nil
}

// timeLeft is how long the run may work under the held claim: the
// contract's wall clock counted from the claim. The contract the run works
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
