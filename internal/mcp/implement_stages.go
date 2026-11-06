package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/agent"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// flight is one implement or verify run in progress: its claim, the base
// branch it starts from, what each stage left for the next, and the record
// forja.status reads, written when each stage starts and when it ends.
// One goroutine owns it.
type flight struct {
	claim domain.Claim
	base  string
	runs  RunStore
	now   func() time.Time
	run   domain.Run
	// round is the correction round the run is; nil for a first run.
	round *round

	// co and what was read from it at the base commit; set by the clone
	// stage.
	co       Checkout
	contract domain.Contract
	issue    domain.Issue
	context  []ContextFile
	// left is how long the run's work may still take: what remains of the
	// contract's wall clock counted from the claim.
	left time.Duration
	// bundles is the directory the agent's commits land in on the control
	// plane, one bundle per attempt; bundle is the current attempt's.
	bundles string
	bundle  string
	// evidence is the report rendered once the gates ran.
	evidence string

	// attempt is the number of the attempt in flight, zero outside the
	// attempts; spent is what its agent used of the model; retry is what
	// its agent is told of the attempt before, nil on the first.
	attempt int
	spent   domain.TokenUsage
	retry   *agent.Retry
}

// newFlight is the run of t's tool under claim c, from t's base branch,
// its claim stage already over.
func newFlight(deps Deps, t runTarget, c domain.Claim, claimed domain.Stage) *flight {
	run := domain.Run{
		ID: c.RunID, Tool: t.tool, Repository: c.Repository, Issue: c.Issue, Client: t.client,
		State: domain.StateRunning, StartedAt: claimed.StartedAt, Stages: []domain.Stage{claimed},
	}
	if c.Supersedes != nil {
		run.Supersedes = c.Supersedes.RunID
	}
	return &flight{claim: c, base: t.base, runs: deps.Runs, now: deps.Now, run: run}
}

// stage records name as started, runs do and records how it ended, the
// records on done. do says whether the run goes on: false with a nil error
// is a run that failed on its own terms, such as a red gate. A record that
// cannot be written stops the run. ctx is the work's.
func (f *flight) stage(_, done context.Context, name domain.StageName, do func() (bool, error)) (bool, error) {
	if err := f.begin(done, name); err != nil {
		return false, err
	}
	ok, err := do()
	ok = ok && err == nil
	f.finish(ok)
	if putErr := f.put(done); putErr != nil {
		return false, errors.Join(err, putErr)
	}
	return ok, err
}

func (f *flight) begin(ctx context.Context, name domain.StageName) error {
	f.run.Stages = append(f.run.Stages, domain.Stage{Name: name, StartedAt: f.now(), Outcome: domain.StageRunning, Attempt: f.attempt})
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

// charge records on the stage in flight what it consumed and adds it to
// the run's totals, which are therefore the sum of its stages'.
func (f *flight) charge(compute time.Duration, tokens domain.TokenUsage) {
	s := &f.run.Stages[len(f.run.Stages)-1]
	s.Compute, s.Tokens = s.Compute+compute, s.Tokens.Add(tokens)
	f.run.Compute, f.run.Tokens = f.run.Compute+compute, f.run.Tokens.Add(tokens)
}

func (f *flight) put(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, FinishTimeout)
	defer cancel()
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
		out = append(out, StageReport{
			Name: string(s.Name), StartedAt: s.StartedAt, FinishedAt: s.FinishedAt, Outcome: string(s.Outcome), Attempt: s.Attempt,
			ComputeMinutes: computeMinutes(s.Compute), TokensIn: s.Tokens.In(), TokensOut: s.Tokens.Output,
		})
	}
	return out
}
