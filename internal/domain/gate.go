package domain

import "time"

// StepStatus is the outcome of one setup step or gate.
type StepStatus string

// The four outcomes of a step. Skipped is a step after the first one that
// did not pass.
const (
	StepPassed   StepStatus = "passed"
	StepFailed   StepStatus = "failed"
	StepSkipped  StepStatus = "skipped"
	StepTimedOut StepStatus = "timed_out"
)

// NoExitCode is the ExitCode of a step that never produced one: it was
// skipped, its deadline fired first, or one of its services was not ready.
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
	// it; Truncated says a stream had more than was kept. For a gate failed
	// by a service that was not ready, the command never ran: the streams
	// are that service's own log, as the control plane read it.
	Stdout    []byte
	Stderr    []byte
	Truncated bool
	// Reason is the control plane's one-line account of a step that failed
	// without an exit code of its own, such as a service that never became
	// ready. Empty when the exit code says it all.
	Reason string
}

// GateReport lists every declared step of a run, in the order the contract
// declares them, whether or not it ran.
type GateReport struct {
	Setup []GateResult
	Gates []GateResult
	// FailedStep names the first step that did not pass, "setup[i]" or the
	// gate name; empty when every step that had to run passed.
	FailedStep string
	// Egress is what the run's gateway refused while the steps ran, as the
	// control plane read it from the gateway's own log.
	Egress EgressReport
	// Registry is what the run's registry proxy served and refused, as the
	// control plane read it from the proxy's own log; empty when the
	// contract declares no registry.
	Registry RegistryReport
}

// OK reports whether the run may be trusted: no step failed or timed out,
// including a gate whose services never became ready.
func (r GateReport) OK() bool {
	return r.FailedStep == ""
}
