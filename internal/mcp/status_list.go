package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// The page size of a list: defaultLimit when the caller sets none, and at
// most maxLimit.
const (
	defaultLimit = 50
	maxLimit     = 200
)

// RunList is forja.status without a run id: one page of the matching runs,
// newest first. NextCursor is absent on the last page.
type RunList struct {
	Runs       []RunSummary `json:"runs"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

// RunSummary is a run as a list reports it: what a platform owner adds up,
// without the stages, gate logs and commits that only a call by id carries.
type RunSummary struct {
	ID         string    `json:"id"`
	Tool       string    `json:"tool"`
	Repository string    `json:"repository"`
	Issue      int       `json:"issue"`
	State      string    `json:"state"`
	StartedAt  time.Time `json:"started_at"`
	// FinishedAt is absent while the run is not terminal.
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// FailureReason is absent unless the control plane gave one.
	FailureReason string `json:"failure_reason,omitempty"`
	TokensIn      int64  `json:"tokens_in"`
	TokensOut     int64  `json:"tokens_out"`
	// ComputeMinutes and EstimatedCostUSDMicros are as by id; the estimate
	// is absent when the server has no price table.
	ComputeMinutes         float64 `json:"compute_minutes"`
	EstimatedCostUSDMicros *int64  `json:"estimated_cost_usd_micros,omitempty"`
	EgressDeniedCount      int     `json:"egress_denied_count"`
}

// listArgument names the first argument of a list the input sets, or is
// empty when it sets none.
func listArgument(in StatusInput) string {
	args := []struct {
		name string
		set  bool
	}{
		{"repository", in.Repository != ""}, {"tool", in.Tool != ""}, {"state", in.State != ""},
		{"since", in.Since != ""}, {"until", in.Until != ""}, {"limit", in.Limit != 0}, {"cursor", in.Cursor != ""},
	}
	for _, a := range args {
		if a.set {
			return a.name
		}
	}
	return ""
}

// listRuns answers a call without a run id.
func listRuns(ctx context.Context, runs RunReader, prices *domain.PriceTable, in StatusInput) (RunList, error) {
	filter, err := runFilter(in)
	if err != nil {
		return RunList{}, err
	}
	limit, err := pageLimit(in.Limit)
	if err != nil {
		return RunList{}, err
	}
	matched, err := runs.List(ctx, filter)
	if err != nil {
		return RunList{}, err
	}
	rest, err := after(ctx, runs, matched, in.Cursor)
	if err != nil {
		return RunList{}, err
	}
	page := rest[:min(limit, len(rest))]
	list := RunList{Runs: runSummaries(page, prices)}
	if len(rest) > len(page) {
		list.NextCursor = page[len(page)-1].ID
	}
	return list, nil
}

// runFilter is the filter the input asks for; a malformed instant, an
// unknown tool or state, or an empty window is an error naming it.
func runFilter(in StatusInput) (domain.RunFilter, error) {
	f := domain.RunFilter{Repository: in.Repository, Tool: domain.Tool(in.Tool), State: domain.RunState(in.State)}
	var err error
	if f.Since, err = instant("since", in.Since); err != nil {
		return domain.RunFilter{}, err
	}
	if f.Until, err = instant("until", in.Until); err != nil {
		return domain.RunFilter{}, err
	}
	if err := f.Validate(); err != nil {
		return domain.RunFilter{}, err
	}
	return f, nil
}

// instant parses the RFC3339 value of the argument name; empty is the zero
// time, which bounds nothing.
func instant(name, value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s %q is not an RFC3339 instant: %w", name, value, err)
	}
	return t, nil
}

func pageLimit(limit int) (int, error) {
	if limit < 0 || limit > maxLimit {
		return 0, fmt.Errorf("limit %d is outside 1 to %d", limit, maxLimit)
	}
	if limit == 0 {
		return defaultLimit, nil
	}
	return limit, nil
}

// after is the runs of sorted that come after the cursor's run in the
// store's order, all of them without a cursor. Keyed on the cursor run's
// start and id, a run recorded between two pages does not shift the walk.
func after(ctx context.Context, runs RunReader, sorted []domain.Run, cursor string) ([]domain.Run, error) {
	if cursor == "" {
		return sorted, nil
	}
	last, err := runs.Get(ctx, cursor)
	if err != nil {
		return nil, fmt.Errorf("cursor %q: %w", cursor, err)
	}
	for i, r := range sorted {
		if domain.CompareNewestFirst(last, r) < 0 {
			return sorted[i:], nil
		}
	}
	return sorted[len(sorted):], nil
}

func runSummaries(runs []domain.Run, prices *domain.PriceTable) []RunSummary {
	out := make([]RunSummary, 0, len(runs))
	for _, r := range runs {
		out = append(out, RunSummary{
			ID: r.ID, Tool: string(r.Tool), Repository: r.Repository, Issue: r.Issue,
			State: string(r.State), StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
			FailureReason: string(r.Reason),
			TokensIn:      r.Tokens.In(), TokensOut: r.Tokens.Output,
			ComputeMinutes:         computeMinutes(r.Compute),
			EstimatedCostUSDMicros: estimate(prices, r),
			EgressDeniedCount:      r.Egress.Count,
		})
	}
	return out
}
