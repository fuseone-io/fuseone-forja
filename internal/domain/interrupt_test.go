package domain_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

func interruptedFixture(state domain.RunState) domain.Run {
	start := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	done := start.Add(time.Minute)
	return domain.Run{
		ID: "run-1", Tool: domain.ToolImplement, Repository: "acme/api", Issue: 7,
		State: state, StartedAt: start,
		Stages: []domain.Stage{
			{Name: domain.StageClaim, StartedAt: start, FinishedAt: &done, Outcome: domain.StageSucceeded},
			{Name: domain.StageAgent, StartedAt: done, Outcome: domain.StageRunning, Attempt: 1},
		},
	}
}

// A run its process left queued or running ends failed, as of the instant
// the next process found it, with every open stage closed failed at that
// instant and the closed ones untouched.
func TestRun_interrupted_inFlightRunEndsFailedWithOpenStagesClosed(t *testing.T) {
	t.Parallel()
	for _, state := range []domain.RunState{domain.StateQueued, domain.StateRunning} {
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			at := time.Date(2026, 11, 1, 13, 0, 0, 0, time.UTC)
			run := interruptedFixture(state)

			got, changed := run.Interrupted(at)

			assertInterrupted(t, got, changed, at)
			if !reflect.DeepEqual(run, interruptedFixture(state)) {
				t.Fatalf("the receiver changed: %+v", run)
			}
		})
	}
}

func assertInterrupted(t *testing.T, got domain.Run, changed bool, at time.Time) {
	t.Helper()
	if !changed || got.State != domain.StateFailed || got.Reason != domain.ReasonInterrupted {
		t.Fatalf("Interrupted = state %q reason %q changed %v, want failed interrupted true", got.State, got.Reason, changed)
	}
	if got.FinishedAt == nil || !got.FinishedAt.Equal(at) {
		t.Fatalf("FinishedAt = %v, want %v", got.FinishedAt, at)
	}
	if closed := interruptedFixture(got.State).Stages[0]; !reflect.DeepEqual(got.Stages[0], closed) {
		t.Fatalf("closed stage = %+v, want it untouched %+v", got.Stages[0], closed)
	}
	open := got.Stages[1]
	if open.Outcome != domain.StageFailed || open.FinishedAt == nil || !open.FinishedAt.Equal(at) {
		t.Fatalf("open stage = %+v, want failed at %v", open, at)
	}
}

// A clock behind the one that started the run never ends it, or an open
// stage, before it started: a duration is never negative.
func TestRun_interrupted_clockBehindStart_neverEndsBeforeItsStart(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		stages int
		want   time.Duration
	}{
		"no stage":                      {stages: 0, want: 0},
		"stages ending after its start": {stages: 2, want: time.Minute},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			run := interruptedFixture(domain.StateRunning)
			run.Stages = run.Stages[:tt.stages]

			got, _ := run.Interrupted(run.StartedAt.Add(-30 * time.Second))

			if want := run.StartedAt.Add(tt.want); got.FinishedAt == nil || !got.FinishedAt.Equal(want) {
				t.Fatalf("FinishedAt = %v, want %v", got.FinishedAt, want)
			}
		})
	}
}

// An open stage the clock is behind ends at its own start.
func TestRun_interrupted_clockBehindOpenStage_stageFinishesAtItsStart(t *testing.T) {
	t.Parallel()
	run := interruptedFixture(domain.StateRunning)

	got, _ := run.Interrupted(run.StartedAt.Add(-30 * time.Second))

	open := got.Stages[1]
	if open.FinishedAt == nil || !open.FinishedAt.Equal(open.StartedAt) {
		t.Fatalf("open stage FinishedAt = %v, want its start %v", open.FinishedAt, open.StartedAt)
	}
}

// A clock past the run's start but behind a stage's never ends the run
// before a stage it holds: the run ends at the latest stage end, the one
// it closed and the one already closed alike.
func TestRun_interrupted_clockBehindAStage_runEndsNoEarlierThanItsStages(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*domain.Run){
		"open stage started after the clock": func(r *domain.Run) {
			early := r.StartedAt.Add(10 * time.Second)
			r.Stages[0].FinishedAt = &early
		},
		"closed stage ended after the clock": func(r *domain.Run) {
			r.Stages = r.Stages[:1]
		},
	}
	for name, shape := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			run := interruptedFixture(domain.StateRunning)
			shape(&run)
			between := run.StartedAt.Add(30 * time.Second)

			got, _ := run.Interrupted(between)

			want := run.StartedAt.Add(time.Minute)
			if got.FinishedAt == nil || !got.FinishedAt.Equal(want) {
				t.Fatalf("FinishedAt = %v, want the latest stage end %v", got.FinishedAt, want)
			}
		})
	}
}

// A run that already ended keeps its outcome, its reason and its finish:
// the process died after recording it.
func TestRun_interrupted_terminalRunUnchanged(t *testing.T) {
	t.Parallel()
	for _, state := range []domain.RunState{domain.StateSucceeded, domain.StateFailed} {
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			run := interruptedFixture(state)
			finished := run.StartedAt.Add(5 * time.Minute)
			run.FinishedAt, run.Reason = &finished, domain.ReasonGateFailed

			got, changed := run.Interrupted(finished.Add(time.Hour))

			if changed || !reflect.DeepEqual(got, run) {
				t.Fatalf("Interrupted = %+v changed %v, want the run unchanged", got, changed)
			}
		})
	}
}
