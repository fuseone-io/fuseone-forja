package mcp

import (
	"math"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// RepositoryMetrics is what every run of one repository that the list's
// filter matches adds up to, whatever the page.
type RepositoryMetrics struct {
	Repository string `json:"repository"`
	Runs       int    `json:"runs"`
	Succeeded  int    `json:"succeeded"`
	Failed     int    `json:"failed"`
	// InFlight counts the queued and running runs.
	InFlight int `json:"in_flight"`
	// AverageDurationSeconds is over the finished runs, to three decimals,
	// and absent when none finished.
	AverageDurationSeconds *float64 `json:"average_duration_seconds,omitempty"`
	// ComputeMinutes is the runs' compute summed, then rounded once.
	ComputeMinutes float64 `json:"compute_minutes"`
	TokensIn       int64   `json:"tokens_in"`
	TokensOut      int64   `json:"tokens_out"`
	// EstimatedCostUSDMicros is the sum of the runs' own estimates, absent
	// when the server has no price table.
	EstimatedCostUSDMicros *int64 `json:"estimated_cost_usd_micros,omitempty"`
	// EgressDeniedCount is every refused attempt of the runs; EgressDenied
	// lists the first of them in the list's order.
	EgressDeniedCount int                     `json:"egress_denied_count"`
	EgressDenied      []RepositoryEgressEvent `json:"egress_denied"`
	// Failures has one row per reason, every reason always, in a fixed
	// order.
	Failures []FailureCount `json:"failures"`
}

// RepositoryEgressEvent is one refused attempt and the run that made it.
type RepositoryEgressEvent struct {
	RunID string    `json:"run_id"`
	At    time.Time `json:"at"`
	Kind  string    `json:"kind"`
	// Destination is host:port only.
	Destination string `json:"destination"`
}

// FailureCount is how many runs failed for Reason. Rate is Count over the
// finished runs, succeeded and failed, to four decimals; 0 when none
// finished.
type FailureCount struct {
	Reason string  `json:"reason"`
	Count  int     `json:"count"`
	Rate   float64 `json:"rate"`
}

// repositoryMetrics is the per-repository wire of runs; empty, never nil.
func repositoryMetrics(runs []domain.Run, prices *domain.PriceTable) []RepositoryMetrics {
	costs := repositoryCosts(runs, prices)
	stats := domain.Summarize(runs)
	out := make([]RepositoryMetrics, 0, len(stats))
	for _, st := range stats {
		out = append(out, RepositoryMetrics{
			Repository: st.Repository, Runs: st.Runs,
			Succeeded: st.Succeeded, Failed: st.Failed, InFlight: st.InFlight,
			AverageDurationSeconds: averageSeconds(st),
			ComputeMinutes:         computeMinutes(st.Compute),
			TokensIn:               st.Tokens.In(), TokensOut: st.Tokens.Output,
			EstimatedCostUSDMicros: costs[st.Repository],
			EgressDeniedCount:      st.EgressDeniedCount,
			EgressDenied:           repositoryEgress(st.EgressDenied),
			Failures:               failureCounts(st),
		})
	}
	return out
}

// repositoryCosts sums each run's estimate by repository, or is nil when
// the server has no price table.
func repositoryCosts(runs []domain.Run, prices *domain.PriceTable) map[string]*int64 {
	if prices == nil {
		return nil
	}
	costs := make(map[string]*int64)
	for _, r := range runs {
		sum, ok := costs[r.Repository]
		if !ok {
			sum = new(int64)
			costs[r.Repository] = sum
		}
		*sum += *estimate(prices, r)
	}
	return costs
}

func averageSeconds(st domain.RepositoryStats) *float64 {
	if st.Finished == 0 {
		return nil
	}
	avg := math.Round(st.WallTime.Seconds()/float64(st.Finished)*1000) / 1000
	return &avg
}

func failureCounts(st domain.RepositoryStats) []FailureCount {
	finished := st.Succeeded + st.Failed
	out := make([]FailureCount, 0, len(domain.FailureReasons()))
	for _, reason := range domain.FailureReasons() {
		n := st.Failures[reason]
		rate := 0.0
		if finished > 0 {
			rate = math.Round(float64(n)/float64(finished)*10000) / 10000
		}
		out = append(out, FailureCount{Reason: string(reason), Count: n, Rate: rate})
	}
	return out
}

func repositoryEgress(denied []domain.RunDenial) []RepositoryEgressEvent {
	out := make([]RepositoryEgressEvent, 0, len(denied))
	for _, d := range denied {
		out = append(out, RepositoryEgressEvent{RunID: d.RunID, At: d.At, Kind: string(d.Kind), Destination: d.Destination})
	}
	return out
}
