package domain_test

import (
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

func step(name string, status domain.StepStatus) domain.GateResult {
	code := 0
	if status != domain.StepPassed {
		code = -1
	}
	if status == domain.StepFailed {
		code = 1
	}
	return domain.GateResult{Name: name, Command: "make " + name, Status: status, ExitCode: code}
}

func TestGateReport_OK_firstNonPassingStepDecides(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		report domain.GateReport
		want   bool
	}{
		{
			name: "every step passed",
			report: domain.GateReport{
				Setup: []domain.GateResult{step("setup[0]", domain.StepPassed)},
				Gates: []domain.GateResult{step("build", domain.StepPassed), step("test", domain.StepPassed)},
			},
			want: true,
		},
		{
			name:   "no step declared",
			report: domain.GateReport{Setup: []domain.GateResult{}, Gates: []domain.GateResult{}},
			want:   true,
		},
		{
			name: "a gate failed and the rest were skipped",
			report: domain.GateReport{
				Setup:      []domain.GateResult{step("setup[0]", domain.StepPassed)},
				Gates:      []domain.GateResult{step("build", domain.StepFailed), step("test", domain.StepSkipped)},
				FailedStep: "build",
			},
			want: false,
		},
		{
			name: "setup failed before any gate",
			report: domain.GateReport{
				Setup:      []domain.GateResult{step("setup[0]", domain.StepPassed), step("setup[1]", domain.StepFailed)},
				Gates:      []domain.GateResult{step("build", domain.StepSkipped)},
				FailedStep: "setup[1]",
			},
			want: false,
		},
		{
			name: "a gate timed out",
			report: domain.GateReport{
				Setup:      []domain.GateResult{},
				Gates:      []domain.GateResult{step("build", domain.StepPassed), step("test", domain.StepTimedOut), step("race", domain.StepSkipped)},
				FailedStep: "test",
			},
			want: false,
		},
		{
			name: "a gate skipped for its services, nothing failed",
			report: domain.GateReport{
				Setup: []domain.GateResult{},
				Gates: []domain.GateResult{step("build", domain.StepPassed), step("integration", domain.StepSkipped), step("test", domain.StepPassed)},
			},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.report.OK(); got != tt.want {
				t.Fatalf("OK() = %v, want %v for FailedStep %q", got, tt.want, tt.report.FailedStep)
			}
		})
	}
}
