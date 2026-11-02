package domain_test

import (
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// What a contract omits takes the PRD's default; what it sets is kept,
// each limit on its own.
func TestLimits_effective_omittedFilledSetKept(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		limits domain.Limits
		want   domain.Limits
	}{
		{name: "both omitted", want: domain.Limits{WallClock: 30 * time.Minute, MaxDiffLines: 400}},
		{name: "wall clock set", limits: domain.Limits{WallClock: 20 * time.Minute}, want: domain.Limits{WallClock: 20 * time.Minute, MaxDiffLines: 400}},
		{name: "max diff lines set", limits: domain.Limits{MaxDiffLines: 50}, want: domain.Limits{WallClock: 30 * time.Minute, MaxDiffLines: 50}},
		{name: "both set", limits: domain.Limits{WallClock: time.Hour, MaxDiffLines: 1200}, want: domain.Limits{WallClock: time.Hour, MaxDiffLines: 1200}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.limits.Effective(); got != tt.want {
				t.Fatalf("Effective(%+v) = %+v, want %+v", tt.limits, got, tt.want)
			}
		})
	}
}
