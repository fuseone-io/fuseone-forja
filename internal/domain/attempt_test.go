package domain_test

import (
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

func TestLimits_maxAttemptsOmitted_defaultsToThree(t *testing.T) {
	t.Parallel()
	if got := (domain.Limits{}).Attempts(); got != 3 {
		t.Fatalf("Attempts() of omitted limits = %d, want the default 3", got)
	}
}

// A contract that sets the limit gets it, below or above the default.
func TestLimits_maxAttemptsSet_thatLimit(t *testing.T) {
	t.Parallel()
	for _, n := range []int{1, 2, 7} {
		if got := (domain.Limits{MaxAttempts: n}).Attempts(); got != n {
			t.Errorf("Attempts() with max_attempts %d = %d, want %d", n, got, n)
		}
	}
}
