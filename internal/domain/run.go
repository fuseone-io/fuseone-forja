// Package domain holds Forja's core types. It does no I/O and imports only the
// standard library.
package domain

import (
	"errors"
	"fmt"
	"time"
)

// Tool is the name of an MCP tool exposed by Forja, exactly as it appears on
// the wire and in FuseOne's Gate.
type Tool string

// The five tools Forja exposes.
const (
	ToolDoctor    Tool = "forja.doctor"
	ToolPlan      Tool = "forja.plan"
	ToolImplement Tool = "forja.implement"
	ToolVerify    Tool = "forja.verify"
	ToolStatus    Tool = "forja.status"
)

// RunState is the lifecycle state of a run.
type RunState string

// The states a run can be in.
const (
	StateQueued    RunState = "queued"
	StateRunning   RunState = "running"
	StateSucceeded RunState = "succeeded"
	StateFailed    RunState = "failed"
)

// ErrRunNotFound is returned by a run reader when no run has the given ID.
var ErrRunNotFound = errors.New("run not found")

// Run is one execution of a tool against an issue of a repository.
type Run struct {
	ID string
	// Tool that produced the run.
	Tool Tool
	// Repository in owner/name form.
	Repository string
	Issue      int
	State      RunState
	StartedAt  time.Time
	// FinishedAt is nil until the run reaches a terminal state.
	FinishedAt *time.Time
	// Supersedes is the id of the run whose expired claim this run took
	// over; empty when the issue was free.
	Supersedes string
	// Egress is what the run's workspace tried to reach and was refused;
	// empty for a run that launched no workspace.
	Egress EgressReport
	// Registry is every request the run's workspace made to its registry
	// proxy; empty for a run whose contract declares no registry.
	Registry RegistryReport
	// Tokens is what the run's model proxy metered; zero for a run that
	// called no model.
	Tokens TokenUsage
	// Compute is the container time the run's workspaces and their service
	// sidecars were up, as the control plane's clock measured it; zero for
	// a run that launched none.
	Compute time.Duration
	// Reason is why a failed run failed, when that is more than its
	// outcome; empty otherwise.
	Reason FailureReason
	// Stages are the steps of an implement run in the order they started;
	// the last one may still be running.
	Stages []Stage
	// Gates is the report of the gates an implement run ran on the agent's
	// head; empty until they ran.
	Gates GateReport
	// BaseSHA is the commit of the base branch the run started from, and
	// ContractSHA the blob of .forja.yaml at that commit, the contract that
	// applied.
	BaseSHA     string
	ContractSHA string
	// HeadSHA is the agent's last commit once the control plane imported
	// it, and Branch the branch it is pushed to.
	HeadSHA string
	Branch  string
	// PullRequest is the pull request the run opened; zero when none.
	PullRequest PullRequest
	// Limits are the limits the run works under, its base contract's with
	// the defaults filled; zero until the contract was read.
	Limits Limits
	// DiffLines is the lines the agent's head adds and deletes against the
	// base, as the control plane counted them; nil until counted.
	DiffLines *int
}

// Validate reports whether the run is well formed: a non-empty ID, a known
// tool and a known state.
func (r Run) Validate() error {
	if r.ID == "" {
		return errors.New("run id is empty")
	}
	if !knownTool(r.Tool) {
		return fmt.Errorf("unknown tool %q", r.Tool)
	}
	if !knownState(r.State) {
		return fmt.Errorf("unknown run state %q", r.State)
	}
	return nil
}

func knownTool(t Tool) bool {
	switch t {
	case ToolDoctor, ToolPlan, ToolImplement, ToolVerify, ToolStatus:
		return true
	}
	return false
}

func knownState(s RunState) bool {
	switch s {
	case StateQueued, StateRunning, StateSucceeded, StateFailed:
		return true
	}
	return false
}
