// Package gates runs a contract's setup steps and gates inside a workspace
// and reports each one's exit code and raw log. It declares the Workspace
// it needs; the Docker implementation lives elsewhere and knows nothing of
// this package.
package gates

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// DefaultWallClock bounds a run whose contract sets no limits.wall_clock.
const DefaultWallClock = 30 * time.Minute

// closeTimeout bounds the removal of a workspace whose run context may
// already be dead.
const closeTimeout = 30 * time.Second

// ExecResult is what one command left behind. The streams are raw bytes;
// the runner never reads them.
type ExecResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

// Workspace is an ephemeral tree the commands run in.
type Workspace interface {
	// Exec runs command with sh -c at the tree root. err is only for a
	// failure to run at all (daemon error, ctx); a non-zero exit code is a
	// result, not an error. On error the result may still carry the bytes
	// read before it.
	Exec(ctx context.Context, command string) (ExecResult, error)
	// Close destroys the workspace. It is idempotent.
	Close(ctx context.Context) error
}

// Launcher creates a workspace from an image with a copy of tree, a
// directory on the control plane, at its root.
type Launcher interface {
	Launch(ctx context.Context, image, tree string) (Workspace, error)
}

// Runner runs a contract's steps in order and stops at the first one that
// does not pass.
type Runner struct {
	Launcher Launcher
	// Now is the clock each step's duration is measured with; a nil clock
	// is a configuration error.
	Now func() time.Time
}

// step is one command to run, with the name the report gives it.
type step struct {
	name    string
	command string
	needs   []string
}

// Run launches c.Image, runs c.Setup then c.Gates and returns a report
// listing every declared step. A failed or timed out step stops the run
// and the rest is skipped; a gate with needs is skipped without stopping
// it. The run's own deadline is a result; a cancellation is an error.
// Close is always called.
func (r *Runner) Run(ctx context.Context, c domain.Contract, tree string) (report domain.GateReport, err error) {
	if r.Now == nil {
		return domain.GateReport{}, errors.New("gates: Runner needs a clock")
	}
	ws, err := r.Launcher.Launch(ctx, c.Image, tree)
	if err != nil {
		return domain.GateReport{}, fmt.Errorf("gates: launch %s: %w", c.Image, err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
		defer cancel()
		if closeErr := ws.Close(closeCtx); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("gates: close workspace: %w", closeErr))
		}
	}()
	run := &execution{ws: ws, now: r.Now}
	report.Setup, err = run.steps(ctx, setupSteps(c))
	if err != nil {
		return domain.GateReport{}, err
	}
	report.Gates, err = run.steps(ctx, gateSteps(c))
	if err != nil {
		return domain.GateReport{}, err
	}
	report.FailedStep = run.failed
	return report, nil
}

func setupSteps(c domain.Contract) []step {
	steps := make([]step, 0, len(c.Setup))
	for i, cmd := range c.Setup {
		steps = append(steps, step{name: fmt.Sprintf("setup[%d]", i), command: cmd})
	}
	return steps
}

func gateSteps(c domain.Contract) []step {
	steps := make([]step, 0, len(c.Gates))
	for _, g := range c.Gates {
		steps = append(steps, step{name: g.Name, command: g.Run, needs: g.Needs})
	}
	return steps
}

// execution is the state of one run: the workspace and the first step
// that did not pass, after which nothing runs.
type execution struct {
	ws     Workspace
	now    func() time.Time
	failed string
}

// steps runs each step in order; the result list is never nil.
func (e *execution) steps(ctx context.Context, steps []step) ([]domain.GateResult, error) {
	results := make([]domain.GateResult, 0, len(steps))
	for _, s := range steps {
		res, err := e.step(ctx, s)
		if err != nil {
			return nil, err
		}
		results = append(results, res)
	}
	return results, nil
}

func (e *execution) step(ctx context.Context, s step) (domain.GateResult, error) {
	if e.failed != "" || len(s.needs) > 0 {
		return skipped(s), nil
	}
	start := e.now()
	out, err := e.ws.Exec(ctx, s.command)
	res := domain.GateResult{Name: s.name, Command: s.command, ExitCode: out.ExitCode, Duration: e.now().Sub(start), Stdout: out.Stdout, Stderr: out.Stderr}
	switch {
	case err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.Status, res.ExitCode = domain.StepTimedOut, domain.NoExitCode
		e.failed = s.name
	case err != nil:
		return domain.GateResult{}, fmt.Errorf("gates: %s: %w", s.name, err)
	case out.ExitCode == 0:
		res.Status = domain.StepPassed
	default:
		res.Status = domain.StepFailed
		e.failed = s.name
	}
	return res, nil
}

func skipped(s step) domain.GateResult {
	return domain.GateResult{Name: s.name, Command: s.command, Status: domain.StepSkipped, ExitCode: domain.NoExitCode}
}
