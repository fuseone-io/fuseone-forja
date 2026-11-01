package domain

import "time"

// StepStatus is the outcome of one setup step or gate.
type StepStatus string

// The four outcomes of a step. Skipped covers both a step after the first
// failure and a gate whose services are not available to the run.
const (
	StepPassed   StepStatus = "passed"
	StepFailed   StepStatus = "failed"
	StepSkipped  StepStatus = "skipped"
	StepTimedOut StepStatus = "timed_out"
)

// NoExitCode is the ExitCode of a step that never produced one: it was
// skipped or its deadline fired first.
const NoExitCode = -1

// GateResult is what one command left behind: its exit code and its raw
// streams, never an interpretation of them.
type GateResult struct {
	// Name is the gate's name, or "setup[i]" for the i-th setup step.
	Name     string
	Command  string
	Status   StepStatus
	ExitCode int
	Duration time.Duration
	// Stdout and Stderr are the tail of each stream as the workspace kept
	// it; Truncated says a stream had more than was kept.
	Stdout    []byte
	Stderr    []byte
	Truncated bool
}

// GateReport lists every declared step of a run, in the order the contract
// declares them, whether or not it ran.
type GateReport struct {
	Setup []GateResult
	Gates []GateResult
	// FailedStep names the first step that did not pass, "setup[i]" or the
	// gate name; empty when every step that had to run passed.
	FailedStep string
}

// OK reports whether the run may be trusted: no step failed or timed out.
// A gate skipped for its services does not decide.
func (r GateReport) OK() bool {
	return r.FailedStep == ""
}
