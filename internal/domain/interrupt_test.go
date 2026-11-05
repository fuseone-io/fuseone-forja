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

			if !changed || got.State != domain.StateFailed || got.Reason != domain.ReasonInterrupted {
				t.Fatalf("Interrupted = state %q reason %q changed %v, want failed interrupted true", got.State, got.Reason, changed)
			}
			if got.FinishedAt == nil || !got.FinishedAt.Equal(at) {
				t.Fatalf("FinishedAt = %v, want %v", got.FinishedAt, at)
			}
			if !reflect.DeepEqual(got.Stages[0], run.Stages[0]) {
				t.Fatalf("closed stage = %+v, want it untouched %+v", got.Stages[0], run.Stages[0])
			}
			open := got.Stages[1]
			if open.Outcome != domain.StageFailed || open.FinishedAt == nil || !open.FinishedAt.Equal(at) {
				t.Fatalf("open stage = %+v, want failed at %v", open, at)
			}
			if run.Stages[1].Outcome != domain.StageRunning || run.Stages[1].FinishedAt != nil || run.State != state {
				t.Fatalf("the receiver changed: %+v", run)
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
