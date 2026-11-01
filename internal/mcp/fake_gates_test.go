package mcp_test

import (
	"context"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// fakeGates answers Run with a scripted report, or an error, and records
// the contract, the tree and the deadline it was given.
type fakeGates struct {
	report   domain.GateReport
	err      error
	calls    int
	contract domain.Contract
	tree     string
	deadline time.Time
	hasDL    bool
}

func (f *fakeGates) Run(ctx context.Context, c domain.Contract, tree string) (domain.GateReport, error) {
	f.calls++
	f.contract, f.tree = c, tree
	f.deadline, f.hasDL = ctx.Deadline()
	if f.err != nil {
		return domain.GateReport{}, f.err
	}
	return f.report, nil
}

func passed(name, command, stdout, stderr string, d time.Duration) domain.GateResult {
	return domain.GateResult{Name: name, Command: command, Status: domain.StepPassed, ExitCode: 0, Duration: d, Stdout: []byte(stdout), Stderr: []byte(stderr)}
}

func skippedStep(name, command string) domain.GateResult {
	return domain.GateResult{Name: name, Command: command, Status: domain.StepSkipped, ExitCode: domain.NoExitCode}
}

// greenReport is the report of doctorContract where every step passed.
func greenReport() domain.GateReport {
	return domain.GateReport{
		Setup: []domain.GateResult{passed("setup[0]", "go mod download", "downloaded\n", "", 3*time.Second)},
		Gates: []domain.GateResult{
			passed("build", "make build", "built\n", "", 500*time.Millisecond),
			passed("test", "make test", "ok\n", "warn\n", 2*time.Second),
		},
	}
}

// redReport is the report of doctorContract where build failed.
func redReport() domain.GateReport {
	return domain.GateReport{
		Setup: []domain.GateResult{passed("setup[0]", "go mod download", "", "", time.Second)},
		Gates: []domain.GateResult{
			{Name: "build", Command: "make build", Status: domain.StepFailed, ExitCode: 2, Duration: 300 * time.Millisecond, Stderr: []byte("undefined: x\n")},
			skippedStep("test", "make test"),
		},
		FailedStep: "build",
	}
}
