package domain

import (
	"errors"
	"time"
)

// StageName is one step of a forja.implement or forja.verify run, as
// forja.status lists it.
type StageName string

// The stages of the implement and verify runs. An implement run goes
// claim, clone, agent, gates, diff, push, pull_request, status, release;
// a correction round comments on the pull request it found instead of
// opening one. A verify run goes claim, clone, diff, status, release when
// its diff is not verifiable, and claim, clone, diff, gates, agent, one
// inversion per admitted candidate, status, release when it has
// production code.
const (
	StageClaim       StageName = "claim"
	StageClone       StageName = "clone"
	StageAgent       StageName = "agent"
	StageGates       StageName = "gates"
	StageDiff        StageName = "diff"
	StageInversion   StageName = "inversion"
	StagePush        StageName = "push"
	StagePullRequest StageName = "pull_request"
	StageComment     StageName = "comment"
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
	// Compute and Tokens are what the stage consumed, recorded when it
	// ends; zero while it runs and for a stage that uses neither.
	Compute time.Duration
	Tokens  TokenUsage
}

// PullRequest is the pull request a run opened or a correction round works
// on. The zero value is none. Author is the login that opened it, Base the
// branch it targets and HeadRepository the owner/name its head lives in;
// they are known only for a pull request read back from GitHub.
type PullRequest struct {
	Number         int
	URL            string
	Author         string
	Base           string
	HeadRepository string
}

// ErrNoCommits means the agent's branch holds no commit beyond the base.
var ErrNoCommits = errors.New("no commits beyond the base")

// ErrNoBranch means the branch asked for is not in the workspace's tree.
var ErrNoBranch = errors.New("branch not in the tree")
