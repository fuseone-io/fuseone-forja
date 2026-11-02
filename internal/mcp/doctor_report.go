package mcp

import (
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// StepReport is one setup step or gate as the maintainer reads it: the
// command, its exit code and the tail of its raw streams, as the workspace
// kept them.
type StepReport struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Status  string `json:"status"`
	// ExitCode is -1 when the step produced none: skipped, timed out, or
	// failed by a service that was not ready.
	ExitCode int `json:"exit_code"`
	// Duration is in nanoseconds, as limits.wall_clock is.
	Duration time.Duration `json:"duration"`
	// Stdout and Stderr are the service's own log when a service failed the
	// step, since its command never ran.
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	// Truncated says the workspace cut a stream to its tail.
	Truncated bool `json:"truncated"`
	// Reason is the control plane's account of a step that failed with no
	// exit code of its own, such as a service that never became ready;
	// absent when the exit code says it all.
	Reason string `json:"reason,omitempty"`
}

// addReport fills the run part of the output from the report.
func (o *DoctorOutput) addReport(report domain.GateReport) {
	o.OK = report.OK()
	o.Setup = stepReports(report.Setup)
	o.Gates = stepReports(report.Gates)
	o.FailedStep = report.FailedStep
}

func stepReports(results []domain.GateResult) []StepReport {
	out := make([]StepReport, 0, len(results))
	for _, r := range results {
		out = append(out, StepReport{
			Name:      r.Name,
			Command:   r.Command,
			Status:    string(r.Status),
			ExitCode:  r.ExitCode,
			Duration:  r.Duration,
			Stdout:    string(r.Stdout),
			Stderr:    string(r.Stderr),
			Truncated: r.Truncated,
			Reason:    r.Reason,
		})
	}
	return out
}
