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

// DefaultReadiness is how long a service has to become ready when the
// Runner sets no Readiness.
const DefaultReadiness = 60 * time.Second

// closeTimeout bounds the removal of a workspace whose run context may
// already be dead.
const closeTimeout = 30 * time.Second

// ExecResult is what one command left behind. The streams are raw bytes,
// bounded by the workspace, which says so in Truncated; the runner never
// reads them.
type ExecResult struct {
	ExitCode  int
	Stdout    []byte
	Stderr    []byte
	Truncated bool
}

// Workspace is an ephemeral tree the commands run in.
type Workspace interface {
	// Exec runs command with sh -c at the tree root. err is only for a
	// failure to run at all (daemon error, ctx); a non-zero exit code is a
	// result, not an error. On error the result may still carry the bytes
	// read before it.
	Exec(ctx context.Context, command string) (ExecResult, error)
	// Egress is what the workspace's gateway refused so far, read from the
	// gateway's own log. It fails once the workspace is closed.
	Egress(ctx context.Context) (domain.EgressReport, error)
	// StartService starts svc as a sidecar the tree reaches by name and
	// waits up to readiness for it to be ready. err is only for a failure
	// to start at all (pull, daemon error, ctx); a service that is not
	// ready in time or exits is a result, not an error.
	StartService(ctx context.Context, name string, svc domain.Service, readiness time.Duration) (ServiceResult, error)
	// Close destroys the workspace and every service started in it. It is
	// idempotent.
	Close(ctx context.Context) error
}

// LaunchSpec is one workspace to create: run RunID's, from Image, with a
// copy of Tree, a directory on the control plane, at its root.
type LaunchSpec struct {
	RunID string
	Image string
	Tree  string
}

// Launcher creates the workspace a LaunchSpec describes.
type Launcher interface {
	Launch(ctx context.Context, spec LaunchSpec) (Workspace, error)
}

// Runner runs a contract's steps in order and stops at the first one that
// does not pass.
type Runner struct {
	Launcher Launcher
	// Now is the clock each step's duration is measured with; a nil clock
	// is a configuration error.
	Now func() time.Time
	// Readiness bounds each service's start; zero is DefaultReadiness.
	Readiness time.Duration
}

// step is one command to run, with the name the report gives it.
type step struct {
	name    string
	command string
	needs   []string
}

// Run launches c.Image as the workspace of runID, runs c.Setup then
// c.Gates and returns a report listing every declared step and what the
// workspace's gateway refused meanwhile. A gate's needs are started from
// c.Services right before it, once per run. A failed or timed out step, or
// a service that is not ready, stops the run and the rest is skipped. The
// run's own deadline is a result; a cancellation is an error. Close is
// always called, after the egress was read, and takes the services down
// with the workspace.
func (r *Runner) Run(ctx context.Context, runID string, c domain.Contract, tree string) (report domain.GateReport, err error) {
	if r.Now == nil {
		return domain.GateReport{}, errors.New("gates: Runner needs a clock")
	}
	ws, err := r.Launcher.Launch(ctx, LaunchSpec{RunID: runID, Image: c.Image, Tree: tree})
	if err != nil {
		return domain.GateReport{}, fmt.Errorf("gates: launch %s: %w", c.Image, err)
	}
	// The run's context may be past its deadline by now; reading the log
	// and removing the workspace each get a context of their own.
	detached := context.WithoutCancel(ctx)
	defer func() {
		closeCtx, cancel := context.WithTimeout(detached, closeTimeout)
		defer cancel()
		if closeErr := ws.Close(closeCtx); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("gates: close workspace: %w", closeErr))
		}
	}()
	run := &execution{ws: ws, now: r.Now, services: newServices(ws, c.Services, r.Readiness)}
	report, err = run.run(ctx, c)
	if err != nil {
		return domain.GateReport{}, err
	}
	egressCtx, cancel := context.WithTimeout(detached, closeTimeout)
	defer cancel()
	if report.Egress, err = ws.Egress(egressCtx); err != nil {
		return domain.GateReport{}, fmt.Errorf("gates: read egress: %w", err)
	}
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

// execution is the state of one run: the workspace, the services started
// in it and the first step that did not pass, after which nothing runs.
type execution struct {
	ws       Workspace
	now      func() time.Time
	services *services
	failed   string
}

// run runs the setup steps, then the gates.
func (e *execution) run(ctx context.Context, c domain.Contract) (domain.GateReport, error) {
	var report domain.GateReport
	var err error
	if report.Setup, err = e.steps(ctx, setupSteps(c)); err != nil {
		return domain.GateReport{}, err
	}
	if report.Gates, err = e.steps(ctx, gateSteps(c)); err != nil {
		return domain.GateReport{}, err
	}
	report.FailedStep = e.failed
	return report, nil
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
	if e.failed != "" {
		return skipped(s), nil
	}
	start := e.now()
	res, err := e.attempt(ctx, s)
	res.Duration = e.now().Sub(start)
	switch {
	case err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		res.Status, res.ExitCode = domain.StepTimedOut, domain.NoExitCode
	case err != nil:
		return domain.GateResult{}, fmt.Errorf("gates: %s: %w", s.name, err)
	}
	if res.Status != domain.StepPassed {
		e.failed = s.name
	}
	return res, nil
}

// attempt starts the step's services, then runs its command only if every
// one of them is ready. A service that is not ready fails the step with
// no exit code, its reason and its log.
func (e *execution) attempt(ctx context.Context, s step) (domain.GateResult, error) {
	res := domain.GateResult{Name: s.name, Command: s.command, Status: domain.StepFailed, ExitCode: domain.NoExitCode}
	svc, err := e.services.start(ctx, s.needs)
	if err != nil || !svc.Ready {
		res.Reason, res.Stdout, res.Truncated = svc.Reason, svc.Log, svc.Truncated
		return res, err
	}
	out, err := e.ws.Exec(ctx, s.command)
	res.ExitCode, res.Stdout, res.Stderr, res.Truncated = out.ExitCode, out.Stdout, out.Stderr, out.Truncated
	if out.ExitCode == 0 {
		res.Status = domain.StepPassed
	}
	return res, err
}

func skipped(s step) domain.GateResult {
	return domain.GateResult{Name: s.name, Command: s.command, Status: domain.StepSkipped, ExitCode: domain.NoExitCode}
}
