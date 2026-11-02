package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

var filterNoon = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

func filterRun() domain.Run {
	return domain.Run{
		ID: "run-1", Tool: domain.ToolImplement, Repository: "acme/api",
		State: domain.StateFailed, StartedAt: filterNoon,
	}
}

func TestRunFilter_match_eachFieldAndBound(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		filter domain.RunFilter
		want   bool
	}{
		{name: "zero filter matches all", filter: domain.RunFilter{}, want: true},
		{name: "same repository", filter: domain.RunFilter{Repository: "acme/api"}, want: true},
		{name: "other repository", filter: domain.RunFilter{Repository: "acme/web"}, want: false},
		{name: "repository is exact, not a prefix", filter: domain.RunFilter{Repository: "acme/ap"}, want: false},
		{name: "same tool", filter: domain.RunFilter{Tool: domain.ToolImplement}, want: true},
		{name: "other tool", filter: domain.RunFilter{Tool: domain.ToolDoctor}, want: false},
		{name: "same state", filter: domain.RunFilter{State: domain.StateFailed}, want: true},
		{name: "other state", filter: domain.RunFilter{State: domain.StateSucceeded}, want: false},
		{name: "since at the start is inclusive", filter: domain.RunFilter{Since: filterNoon}, want: true},
		{name: "since before the start", filter: domain.RunFilter{Since: filterNoon.Add(-time.Nanosecond)}, want: true},
		{name: "since after the start", filter: domain.RunFilter{Since: filterNoon.Add(time.Nanosecond)}, want: false},
		{name: "until at the start is exclusive", filter: domain.RunFilter{Until: filterNoon}, want: false},
		{name: "until after the start", filter: domain.RunFilter{Until: filterNoon.Add(time.Nanosecond)}, want: true},
		{name: "until before the start", filter: domain.RunFilter{Until: filterNoon.Add(-time.Nanosecond)}, want: false},
		{name: "every field matching", filter: domain.RunFilter{
			Repository: "acme/api", Tool: domain.ToolImplement, State: domain.StateFailed,
			Since: filterNoon, Until: filterNoon.Add(time.Second),
		}, want: true},
		{name: "every field matching but one", filter: domain.RunFilter{
			Repository: "acme/api", Tool: domain.ToolImplement, State: domain.StateRunning,
			Since: filterNoon, Until: filterNoon.Add(time.Second),
		}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.filter.Match(filterRun()); got != tt.want {
				t.Fatalf("Match = %v, want %v for %+v", got, tt.want, tt.filter)
			}
		})
	}
}

func TestRunFilter_validate_rejectsUnknownValuesAndEmptyWindow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		filter  domain.RunFilter
		wantErr string
	}{
		{name: "zero filter", filter: domain.RunFilter{}},
		{name: "known tool and state", filter: domain.RunFilter{Tool: domain.ToolDoctor, State: domain.StateQueued}},
		{name: "since before until", filter: domain.RunFilter{Since: filterNoon, Until: filterNoon.Add(time.Nanosecond)}},
		{name: "since alone", filter: domain.RunFilter{Since: filterNoon}},
		{name: "until alone", filter: domain.RunFilter{Until: filterNoon}},
		{name: "unknown tool", filter: domain.RunFilter{Tool: "forja.deploy"}, wantErr: "forja.deploy"},
		{name: "unknown state", filter: domain.RunFilter{State: "paused"}, wantErr: "paused"},
		{name: "since equal to until", filter: domain.RunFilter{Since: filterNoon, Until: filterNoon}, wantErr: "since"},
		{name: "since after until", filter: domain.RunFilter{Since: filterNoon.Add(time.Second), Until: filterNoon}, wantErr: "until"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.filter.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate = %v, want an error naming %q", err, tt.wantErr)
			}
		})
	}
}

func TestCompareNewestFirst_laterStartFirstThenHigherID(t *testing.T) {
	t.Parallel()
	older := domain.Run{ID: "z", StartedAt: filterNoon}
	newer := domain.Run{ID: "a", StartedAt: filterNoon.Add(time.Second)}
	tieLow := domain.Run{ID: "b", StartedAt: filterNoon}
	if domain.CompareNewestFirst(newer, older) >= 0 || domain.CompareNewestFirst(older, newer) <= 0 {
		t.Fatal("a later start must sort first whatever the ids")
	}
	if domain.CompareNewestFirst(older, tieLow) >= 0 || domain.CompareNewestFirst(tieLow, older) <= 0 {
		t.Fatal("on the same start the higher id must sort first")
	}
	if domain.CompareNewestFirst(older, older) != 0 {
		t.Fatal("a run must compare equal to itself")
	}
}
