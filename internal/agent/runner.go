// Package agent drives the coding agent inside a run's workspace: it hands
// the agent its prompt, runs it under the run's deadline, stops it when the
// model proxy reaches the token cap, and reads what the proxies recorded.
// It declares the Workspace it needs; the Docker implementation lives
// elsewhere and knows nothing of this package.
package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// closeTimeout bounds the reads and the removal of a workspace whose run
// context may already be dead.
const closeTimeout = 30 * time.Second

// ExecResult is what one command left behind. The runner reads only the
// exit code: the agent's streams are the workspace's to write and are
// neither logged nor kept.
type ExecResult struct {
	ExitCode  int
	Stdout    []byte
	Stderr    []byte
	Truncated bool
}

// ModelReport is what the run's model proxy metered, read from its own log.
type ModelReport struct {
	Tokens domain.TokenUsage
	Capped bool
}

// Workspace is an agent's ephemeral tree with its model proxy.
type Workspace interface {
	// Exec runs command with sh -c at the tree root. err is only for a
	// failure to run at all, including ctx ending first.
	Exec(ctx context.Context, command string) (ExecResult, error)
	// WriteFile writes data to an absolute path whose directory exists.
	WriteFile(ctx context.Context, path string, data []byte) error
	// Model is what the model proxy metered so far.
	Model(ctx context.Context) (ModelReport, error)
	// AwaitCap returns nil once the model proxy reached the run's cap, and
	// an error when ctx ends first or the proxy's log ends without it.
	AwaitCap(ctx context.Context) error
	// Egress is what the gateway refused so far.
	Egress(ctx context.Context) (domain.EgressReport, error)
	// ExportCommits writes to dst, a path on the control plane, a git
	// bundle of branch beyond the commit base, as the tree holds it. An
	// agent that committed nothing wraps domain.ErrNoCommits. It needs the
	// workspace still running.
	ExportCommits(ctx context.Context, base, branch, dst string) error
	// Halt stops the tree's processes and leaves the proxies' logs to read.
	Halt(ctx context.Context) error
	// Close destroys the workspace and its proxies. It is idempotent.
	Close(ctx context.Context) error
}

// Launch is one agent workspace of run RunID from Image with a copy of Tree
// at its root. Spent is the tokens the run already used, input and output,
// which its model proxy subtracts from the run's cap.
type Launch struct {
	RunID string
	Image string
	Tree  string
	Spent int64
}

// ErrCapSpent is what a Launcher's error wraps when Spent already reaches
// the run's cap, so no workspace was created.
var ErrCapSpent = errors.New("agent: the run's token cap is spent")

// Launcher creates the agent workspace of one launch.
type Launcher interface {
	LaunchAgent(ctx context.Context, l Launch) (Workspace, error)
}

// Result is how the agent run ended and what the proxies recorded for it.
// ExitCode is domain.NoExitCode when the agent was stopped, by the
// deadline (TimedOut) or by the cap (Capped). Exported says the agent's
// commits are in the task's Bundle. Compute is the time the workspace was
// up, on every path, a failed launch included.
type Result struct {
	ExitCode int
	TimedOut bool
	Capped   bool
	Exported bool
	Tokens   domain.TokenUsage
	Egress   domain.EgressReport
	Compute  time.Duration
}

// Runner runs the agent once per Task.
type Runner struct {
	Launcher Launcher
	// Now is the clock the workspace's compute is measured with; a nil
	// clock is a configuration error.
	Now func() time.Time
}

// Run launches the workspace, writes the prompt to PromptPath and runs the
// agent until it exits, ctx ends or the proxy reaches the cap. An agent
// that exited 0 on its own has its commits exported to the task's Bundle
// while the workspace still runs. Then it halts the workspace, reads the
// usage and the egress, and closes it, on every path. The deadline is a
// result; a cancellation is an error. So is the cap: spent before the
// launch, no workspace exists and nothing more is metered. The compute
// runs from just before the launch until Close returned, or until a
// failed launch returned.
func (r *Runner) Run(ctx context.Context, t Task) (Result, error) {
	l := launch{Launch: Launch{RunID: t.RunID, Image: t.Contract.Image, Tree: t.Tree, Spent: t.Spent.Total()}, prompt: Render(t)}
	return r.session(ctx, l, func(ctx context.Context, ws Workspace, res *Result) error { return export(ctx, ws, t, res) })
}

// export brings the commits of an agent that exited 0 on its own out to
// the task's Bundle. Nothing to export is a result; so is the deadline
// ending the export.
func export(ctx context.Context, ws Workspace, t Task, res *Result) error {
	err := ws.ExportCommits(ctx, t.BaseSHA, t.Branch, t.Bundle)
	switch {
	case err == nil:
		res.Exported = true
	case errors.Is(err, domain.ErrNoCommits):
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.TimedOut = true
	default:
		return fmt.Errorf("agent: export commits: %w", err)
	}
	return nil
}

// execute writes the prompt and runs the agent, classifying how it ended.
func execute(ctx context.Context, ws Workspace, prompt []byte) (Result, error) {
	res := Result{ExitCode: domain.NoExitCode}
	if err := ws.WriteFile(ctx, PromptPath, prompt); err != nil {
		return res, fmt.Errorf("agent: write prompt: %w", err)
	}
	out, capped, err := execUnderCap(ctx, ws)
	res.Capped = capped
	switch {
	case err == nil:
		res.ExitCode = out.ExitCode
	case capped && ctx.Err() == nil:
		// The cap stopped the agent: that is the outcome, not a failure.
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.TimedOut = true
	default:
		return res, fmt.Errorf("agent: exec: %w", err)
	}
	return res, nil
}

// execUnderCap runs the agent while a watcher follows the proxy's log; the
// cap cancels the agent. The watcher is stopped once the agent returns,
// and capped says whether it saw the cap.
func execUnderCap(ctx context.Context, ws Workspace) (ExecResult, bool, error) {
	execCtx, stopExec := context.WithCancel(ctx)
	defer stopExec()
	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	capped := make(chan bool, 1)
	go func() {
		hit := ws.AwaitCap(watchCtx) == nil
		if hit {
			stopExec()
		}
		capped <- hit
	}()
	out, err := ws.Exec(execCtx, Command())
	stopWatch()
	return out, <-capped, err
}

// collect halts the workspace so nothing reaches the proxies any more,
// then reads what they recorded. The proxy's own log decides the cap too:
// an agent may have exited on a refusal before the watcher saw it.
func collect(ctx context.Context, ws Workspace, res *Result) error {
	ctx, cancel := context.WithTimeout(ctx, closeTimeout)
	defer cancel()
	var errs []error
	if err := ws.Halt(ctx); err != nil {
		errs = append(errs, fmt.Errorf("agent: halt workspace: %w", err))
	}
	if report, err := ws.Model(ctx); err != nil {
		errs = append(errs, fmt.Errorf("agent: read usage: %w", err))
	} else {
		res.Tokens, res.Capped = report.Tokens, res.Capped || report.Capped
	}
	if egress, err := ws.Egress(ctx); err != nil {
		errs = append(errs, fmt.Errorf("agent: read egress: %w", err))
	} else {
		res.Egress = egress
	}
	return errors.Join(errs...)
}
