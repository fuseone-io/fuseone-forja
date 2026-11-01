package mcp

import (
	"fmt"
	"strings"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// maxLogBytes is how much of each stream a step report carries. The tail
// is what a maintainer reads first on a red gate; the whole log stays in
// the domain report.
const maxLogBytes = 64 << 10

// StepReport is one setup step or gate as the maintainer reads it: the
// command, its exit code and the tail of its raw streams.
type StepReport struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Status  string `json:"status"`
	// ExitCode is -1 when the step produced none: skipped or timed out.
	ExitCode int `json:"exit_code"`
	// Duration is in nanoseconds, as limits.wall_clock is.
	Duration time.Duration `json:"duration"`
	Stdout   string        `json:"stdout"`
	Stderr   string        `json:"stderr"`
	// Truncated says a stream was cut to its last maxLogBytes.
	Truncated bool `json:"truncated"`
}

// addReport fills the run part of the output from the report and the
// contract it ran.
func (o *DoctorOutput) addReport(c domain.Contract, report domain.GateReport) {
	o.OK = report.OK()
	o.Setup = stepReports(report.Setup)
	o.Gates = stepReports(report.Gates)
	o.FailedStep = report.FailedStep
	o.Warnings = needsWarnings(c)
}

func stepReports(results []domain.GateResult) []StepReport {
	out := make([]StepReport, 0, len(results))
	for _, r := range results {
		stdout, cutOut := tail(r.Stdout)
		stderr, cutErr := tail(r.Stderr)
		out = append(out, StepReport{
			Name:      r.Name,
			Command:   r.Command,
			Status:    string(r.Status),
			ExitCode:  r.ExitCode,
			Duration:  r.Duration,
			Stdout:    stdout,
			Stderr:    stderr,
			Truncated: cutOut || cutErr,
		})
	}
	return out
}

// tail keeps the last maxLogBytes of a stream and says whether it cut.
func tail(b []byte) (string, bool) {
	if len(b) <= maxLogBytes {
		return string(b), false
	}
	return string(b[len(b)-maxLogBytes:]), true
}

// needsWarnings names every gate the run skipped for its services.
func needsWarnings(c domain.Contract) []string {
	warnings := []string{}
	for _, g := range c.Gates {
		if len(g.Needs) == 0 {
			continue
		}
		warnings = append(warnings, fmt.Sprintf("gate %s needs %s: skipped, services land in a later slice", g.Name, strings.Join(g.Needs, ", ")))
	}
	return warnings
}
