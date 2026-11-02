package domain

import (
	"cmp"
	"slices"
	"time"
)

// ReasonUnspecified is the metrics-only key a failed run is counted under
// when the control plane gave it no reason, or one outside the known set.
// No run carries it.
const ReasonUnspecified FailureReason = "unspecified"

// FailureReasons is every key a failure is counted under, in the order the
// metrics report them.
func FailureReasons() []FailureReason {
	return []FailureReason{
		ReasonGateFailed, ReasonTokenCap, ReasonWallClock,
		ReasonDiffTooLarge, ReasonNoCommits, ReasonUnspecified,
	}
}

// RunDenial is one refused egress attempt with the run whose workspace
// made it.
type RunDenial struct {
	RunID string
	EgressDenial
}

// RepositoryStats is what the runs of one repository add up to.
type RepositoryStats struct {
	Repository string
	// Runs is Succeeded + Failed + InFlight; InFlight counts queued and
	// running runs.
	Runs, Succeeded, Failed, InFlight int
	// Finished counts the runs with a FinishedAt, and WallTime sums their
	// FinishedAt - StartedAt.
	Finished int
	WallTime time.Duration
	Compute  time.Duration
	Tokens   TokenUsage
	// Failures counts the failed runs by reason; their sum is Failed.
	Failures map[FailureReason]int
	// EgressDeniedCount sums every run's count; EgressDenied lists the
	// runs' listed denials in input order, the first MaxEgressDenials.
	EgressDeniedCount int
	EgressDenied      []RunDenial
}

// Summarize folds runs into one RepositoryStats per repository, sorted by
// repository. It is empty, never nil, for no runs.
func Summarize(runs []Run) []RepositoryStats {
	byRepo := make(map[string]*RepositoryStats)
	for _, r := range runs {
		st, ok := byRepo[r.Repository]
		if !ok {
			st = &RepositoryStats{Repository: r.Repository, Failures: make(map[FailureReason]int), EgressDenied: []RunDenial{}}
			byRepo[r.Repository] = st
		}
		st.add(r)
	}
	out := make([]RepositoryStats, 0, len(byRepo))
	for _, st := range byRepo {
		out = append(out, *st)
	}
	slices.SortFunc(out, func(a, b RepositoryStats) int { return cmp.Compare(a.Repository, b.Repository) })
	return out
}

func (st *RepositoryStats) add(r Run) {
	st.Runs++
	switch r.State {
	case StateSucceeded:
		st.Succeeded++
	case StateFailed:
		st.Failed++
		st.Failures[failureKey(r.Reason)]++
	default:
		st.InFlight++
	}
	if r.FinishedAt != nil {
		st.Finished++
		st.WallTime += r.FinishedAt.Sub(r.StartedAt)
	}
	st.Compute += r.Compute
	st.Tokens = st.Tokens.Add(r.Tokens)
	st.EgressDeniedCount += r.Egress.Count
	for _, d := range r.Egress.Denied {
		if len(st.EgressDenied) == MaxEgressDenials {
			break
		}
		st.EgressDenied = append(st.EgressDenied, RunDenial{RunID: r.ID, EgressDenial: d})
	}
}

// failureKey is the key a failed run with reason is counted under.
func failureKey(reason FailureReason) FailureReason {
	if slices.Contains(FailureReasons(), reason) {
		return reason
	}
	return ReasonUnspecified
}
