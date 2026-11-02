package domain

// TokenUsage is what a run consumed of the model, as the model proxy read
// it from the responses it forwarded. The zero value is no tokens.
type TokenUsage struct {
	Input         int64
	CacheCreation int64
	CacheRead     int64
	Output        int64
}

// Add is u and o summed field by field.
func (u TokenUsage) Add(o TokenUsage) TokenUsage {
	return TokenUsage{
		Input:         u.Input + o.Input,
		CacheCreation: u.CacheCreation + o.CacheCreation,
		CacheRead:     u.CacheRead + o.CacheRead,
		Output:        u.Output + o.Output,
	}
}

// In is every input token: the uncached input and both cache fields.
func (u TokenUsage) In() int64 {
	return u.Input + u.CacheCreation + u.CacheRead
}

// Total is every token, input and output, which is what the cap counts.
func (u TokenUsage) Total() int64 {
	return u.In() + u.Output
}

// FailureReason says why a failed run failed, when the control plane knows
// a reason more specific than the run's own outcome.
type FailureReason string

// The reasons the control plane gives for a failed run.
const (
	// ReasonTokenCap is a run whose model proxy refused it more tokens.
	ReasonTokenCap FailureReason = "token_cap"
	// ReasonGateFailed is a run whose gates did not all pass on the agent's
	// head.
	ReasonGateFailed FailureReason = "gate_failed"
	// ReasonNoCommits is a run whose agent exited 0 without committing.
	ReasonNoCommits FailureReason = "no_commits"
	// ReasonInversionUnavailable is a verify run whose diff has production
	// code and whose QA agent left no candidate the control plane admits.
	ReasonInversionUnavailable FailureReason = "inversion_unavailable"
	// ReasonInversionInconclusive is a verify run with an inversion whose
	// gates neither went red at a gate that ran nor all passed, such as a
	// setup step or a service that failed.
	ReasonInversionInconclusive FailureReason = "inversion_inconclusive"
	// ReasonWallClock is a run that its contract's limits.wall_clock ended.
	ReasonWallClock FailureReason = "wall_clock"
	// ReasonDiffTooLarge is a run whose diff against the base changed more
	// lines than its contract's limits.max_diff_lines.
	ReasonDiffTooLarge FailureReason = "diff_too_large"
)
