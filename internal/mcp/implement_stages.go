package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// flight is one implement run in progress: its claim, the base branch it
// starts from, what each stage left for the next, and the record
// forja.status reads, written when each stage starts and when it ends.
// One goroutine owns it.
type flight struct {
	claim domain.Claim
	base  string
	runs  RunStore
	now   func() time.Time
	run   domain.Run

	// co and what was read from it at the base commit; set by the clone
	// stage.
	co       Checkout
	contract domain.Contract
	issue    domain.Issue
	context  []ContextFile
	// left is how long the run's work may still take: what remains of the
	// contract's wall clock counted from the claim.
	left time.Duration
	// bundle is where the agent's commits land on the control plane.
	bundle string
	// evidence is the report rendered once the gates ran.
	evidence string
}

// newFlight is the run of claim c, its claim stage already over.
func newFlight(deps Deps, c domain.Claim, base string, claimed domain.Stage) *flight {
	run := domain.Run{
		ID: c.RunID, Tool: domain.ToolImplement, Repository: c.Repository, Issue: c.Issue,
		State: domain.StateRunning, StartedAt: claimed.StartedAt, Stages: []domain.Stage{claimed},
	}
	if c.Supersedes != nil {
		run.Supersedes = c.Supersedes.RunID
	}
	return &flight{claim: c, base: base, runs: deps.Runs, now: deps.Now, run: run}
}

// stage records name as started, runs do and records how it ended. do
// says whether the run goes on: false with a nil error is a run that
// failed on its own terms, such as a red gate. A record that cannot be
// written stops the run.
func (f *flight) stage(ctx context.Context, name domain.StageName, do func() (bool, error)) (bool, error) {
	if err := f.begin(ctx, name); err != nil {
		return false, err
	}
	ok, err := do()
	ok = ok && err == nil
	f.finish(ok)
	if putErr := f.put(ctx); putErr != nil {
		return false, errors.Join(err, putErr)
	}
	return ok, err
}

func (f *flight) begin(ctx context.Context, name domain.StageName) error {
	f.run.Stages = append(f.run.Stages, domain.Stage{Name: name, StartedAt: f.now(), Outcome: domain.StageRunning})
	return f.put(ctx)
}

// finish ends the stage in flight.
func (f *flight) finish(ok bool) {
	s := &f.run.Stages[len(f.run.Stages)-1]
	at := f.now()
	s.FinishedAt, s.Outcome = &at, domain.StageSucceeded
	if !ok {
		s.Outcome = domain.StageFailed
	}
}

func (f *flight) put(ctx context.Context) error {
	if err := f.runs.Put(ctx, f.run); err != nil {
		return fmt.Errorf("record run: %w", err)
	}
	return nil
}

// closeCheckout discards the clone, once there is one. The run's outcome
// is its stages'; a directory that could not be removed changes none of
// it.
func (f *flight) closeCheckout() {
	if f.co != nil {
		_ = f.co.Close()
	}
}

// claimStage is the claim stage, timed around the call that took it.
func claimStage(started, finished time.Time) domain.Stage {
	return domain.Stage{Name: domain.StageClaim, StartedAt: started, FinishedAt: &finished, Outcome: domain.StageSucceeded}
}

func stageReports(stages []domain.Stage) []StageReport {
	out := make([]StageReport, 0, len(stages))
	for _, s := range stages {
		out = append(out, StageReport{Name: string(s.Name), StartedAt: s.StartedAt, FinishedAt: s.FinishedAt, Outcome: string(s.Outcome)})
	}
	return out
}
