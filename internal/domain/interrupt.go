package domain

import (
	"slices"
	"time"
)

// ReasonInterrupted is a run whose process stopped before it ended: the
// next process found its claim left behind and closed it.
const ReasonInterrupted FailureReason = "interrupted"

// Interrupted is the run as the next process records it when the one that
// ran it stopped first: a queued or running run fails at at, and every
// stage still running fails at at with it. A run that already ended is
// returned as is, and changed reports whether the run was in flight.
func (r Run) Interrupted(at time.Time) (run Run, changed bool) {
	if r.State != StateQueued && r.State != StateRunning {
		return r, false
	}
	r.State, r.Reason = StateFailed, ReasonInterrupted
	r.FinishedAt = &at
	r.Stages = slices.Clone(r.Stages)
	for i := range r.Stages {
		if r.Stages[i].Outcome == StageRunning {
			finished := at
			r.Stages[i].Outcome, r.Stages[i].FinishedAt = StageFailed, &finished
		}
	}
	return r, true
}
