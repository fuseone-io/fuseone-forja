package mcp_test

import (
	"context"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// fakeGates answers Run with a scripted report, or an error, and records
// the run id, the contract, the tree and the deadline it was given. With
// reports set, the n-th call answers reports[n], the last one once they
// run out. Like the real runner, an error still comes with the report's
// compute.
type fakeGates struct {
	report    domain.GateReport
	reports   []domain.GateReport
	err       error
	calls     int
	runID     string
	contract  domain.Contract
	tree      string
	trees     []string
	deadline  time.Time
	hasDL     bool
	deadlines []time.Time
	// waitDeadline makes every call return only once its context ended, as
	// gates that ran past the run's wall clock do.
	waitDeadline bool
	// log, when set, records "gates" in the run's call order; during runs
	// while the gates would.
	log    *callLog
	during func()
	// byTree answers for a tree over report and reports, and errByTree
	// fails it; contracts are what every call was given, in order.
	byTree    map[string]domain.GateReport
	errByTree map[string]error
	contracts []domain.Contract
	// untilDeadline runs the first gate of that tree until the deadline
	// and reports it timed out, as the real runner does.
	untilDeadline string
}

func (f *fakeGates) Run(ctx context.Context, runID string, c domain.Contract, tree string) (domain.GateReport, error) {
	if f.log != nil {
		f.log.add("gates")
	}
	if f.during != nil {
		f.during()
	}
	if f.waitDeadline {
		<-ctx.Done()
	}
	f.calls++
	f.runID, f.contract, f.tree = runID, c, tree
	f.trees, f.contracts = append(f.trees, tree), append(f.contracts, c)
	f.deadline, f.hasDL = ctx.Deadline()
	f.deadlines = append(f.deadlines, f.deadline)
	if tree == f.untilDeadline {
		<-ctx.Done()
		return domain.GateReport{
			Gates:      []domain.GateResult{{Name: "build", Command: "make build", Status: domain.StepTimedOut, ExitCode: domain.NoExitCode}},
			FailedStep: "build",
		}, nil
	}
	report := f.report
	if len(f.reports) > 0 {
		report = f.reports[min(f.calls, len(f.reports))-1]
	}
	if r, ok := f.byTree[tree]; ok {
		report = r
	}
	if err := f.errByTree[tree]; err != nil {
		return domain.GateReport{Compute: report.Compute}, err
	}
	if f.err != nil {
		return domain.GateReport{Compute: report.Compute}, f.err
	}
	return report, nil
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

// failedAt is the report of a run whose step failed with exit code 1 and
// stderr as its raw stream, every step before it passed.
func failedAt(step, stderr string) domain.GateReport {
	return domain.GateReport{
		Setup: []domain.GateResult{passed("setup[0]", "go mod download", "", "", time.Second)},
		Gates: []domain.GateResult{
			passed("build", "make build", "built\n", "", time.Second),
			{Name: step, Command: "make " + step, Status: domain.StepFailed, ExitCode: 1, Duration: time.Second, Stdout: []byte(step + " ran\n"), Stderr: []byte(stderr)},
		},
		FailedStep: step,
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
