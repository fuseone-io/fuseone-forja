package mcp

import (
	"context"

	"github.com/fuseone-io/fuseone-forja/internal/agent"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// AttemptReport is one attempt whose gates ran: the head they ran on, the
// step that did not pass, absent when every one did, what that attempt's
// agent used of the model, and the report of its gates.
type AttemptReport struct {
	Number     int          `json:"number"`
	HeadSHA    string       `json:"head_sha,omitempty"`
	FailedStep string       `json:"failed_step,omitempty"`
	TokensIn   int64        `json:"tokens_in"`
	TokensOut  int64        `json:"tokens_out"`
	Setup      []StepReport `json:"setup"`
	Gates      []StepReport `json:"gates"`
}

// attempts runs the agent, then the gates on its head, until they are
// green or the base contract's attempt limit is spent. Only a red gate
// goes back to the agent, and only while the run's deadline has not
// passed: ctx is the run's, shared by every attempt. Nothing is pushed
// before the attempts end.
func (h implementer) attempts(ctx, done context.Context, f *flight) (bool, error) {
	defer func() { f.attempt = 0 }()
	for f.attempt = 1; ; f.attempt++ {
		ok, err := h.attempt(ctx, done, f)
		if ok || err != nil || !f.retryable(ctx) {
			return ok, err
		}
		f.retry = &agent.Retry{Attempt: f.attempt + 1, MaxAttempts: f.run.MaxAttempts, Failed: failedResult(f.run.Gates)}
		f.run.Reason = ""
	}
}

// attempt is one pass of the agent and, when it delivered a new head, of
// the gates on it.
func (h implementer) attempt(ctx, done context.Context, f *flight) (bool, error) {
	if ok, err := h.agentStage(ctx, done, f); !ok || err != nil {
		return false, err
	}
	return h.gatesStage(ctx, done, f)
}

// retryable says the attempt just over ended on a red gate, another one is
// allowed, and the run's deadline has not passed.
func (f *flight) retryable(ctx context.Context) bool {
	return f.run.Reason == domain.ReasonGateFailed && f.attempt < f.run.MaxAttempts && ctx.Err() == nil
}

// failedResult is the step of report its FailedStep names, as the gates
// runner kept it.
func failedResult(report domain.GateReport) domain.GateResult {
	for _, r := range append(append([]domain.GateResult(nil), report.Setup...), report.Gates...) {
		if r.Name == report.FailedStep {
			return r
		}
	}
	return domain.GateResult{Name: report.FailedStep, ExitCode: domain.NoExitCode}
}

// currentAttempt is the attempt the latest attempt stage belongs to: the
// one in flight, or the last one; zero before the first agent stage.
func currentAttempt(stages []domain.Stage) int {
	for i := len(stages) - 1; i >= 0; i-- {
		if stages[i].Attempt > 0 {
			return stages[i].Attempt
		}
	}
	return 0
}

func attemptReports(attempts []domain.Attempt) []AttemptReport {
	out := make([]AttemptReport, 0, len(attempts))
	for _, a := range attempts {
		out = append(out, AttemptReport{
			Number: a.Number, HeadSHA: a.HeadSHA, FailedStep: a.Gates.FailedStep,
			TokensIn: a.Tokens.In(), TokensOut: a.Tokens.Output,
			Setup: stepReports(a.Gates.Setup), Gates: stepReports(a.Gates.Gates),
		})
	}
	return out
}
