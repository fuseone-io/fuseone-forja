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
