package domain

import (
	"errors"
	"time"
)

// StageName is one step of a forja.implement or forja.verify run, as
// forja.status lists it.
type StageName string

// The stages of the implement and verify runs. An implement run goes
// claim, clone, agent, gates, push, pull_request, status, release; a
// verify run claim, clone, diff, status, release.
const (
	StageClaim       StageName = "claim"
	StageClone       StageName = "clone"
	StageAgent       StageName = "agent"
	StageGates       StageName = "gates"
	StageDiff        StageName = "diff"
	StagePush        StageName = "push"
	StagePullRequest StageName = "pull_request"
	StageStatus      StageName = "status"
	StageRelease     StageName = "release"
)

// StageOutcome is where a stage stands.
type StageOutcome string

// A stage is running from the moment it starts until it ends, one way or
// the other.
const (
	StageRunning   StageOutcome = "running"
	StageSucceeded StageOutcome = "succeeded"
	StageFailed    StageOutcome = "failed"
)

// Stage is one step of a run with when it started and, once it ended, when
// and how.
type Stage struct {
	Name      StageName
	StartedAt time.Time
	// FinishedAt is nil while the stage runs.
	FinishedAt *time.Time
	Outcome    StageOutcome
}

// PullRequest is the pull request a run opened. The zero value is none.
type PullRequest struct {
	Number int
	URL    string
}

// ErrNoCommits means the agent's branch holds no commit beyond the base.
var ErrNoCommits = errors.New("no commits beyond the base")
