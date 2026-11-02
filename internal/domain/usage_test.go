package domain_test

import (
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// Each field is a distinct power of two, so a sum that drops or doubles
// one field gives a value no other mistake gives.
var usage = domain.TokenUsage{Input: 1, CacheCreation: 2, CacheRead: 4, Output: 8}

func TestTokenUsage_in_isInputPlusBothCacheFields(t *testing.T) {
	t.Parallel()
	if got := usage.In(); got != 7 {
		t.Fatalf("In() = %d, want 7: input 1 + cache creation 2 + cache read 4", got)
	}
}

func TestTokenUsage_total_isEveryField(t *testing.T) {
	t.Parallel()
	if got := usage.Total(); got != 15 {
		t.Fatalf("Total() = %d, want 15: in 7 + output 8", got)
	}
}

func TestTokenUsage_add_fieldByField(t *testing.T) {
	t.Parallel()
	other := domain.TokenUsage{Input: 16, CacheCreation: 32, CacheRead: 64, Output: 128}

	got := usage.Add(other)
	want := domain.TokenUsage{Input: 17, CacheCreation: 34, CacheRead: 68, Output: 136}
	if got != want {
		t.Fatalf("Add = %+v, want %+v", got, want)
	}
	if usage.Total() != 15 {
		t.Fatalf("Add changed its receiver: %+v", usage)
	}
}

func TestTokenUsage_zeroValue_isNoTokens(t *testing.T) {
	t.Parallel()
	var zero domain.TokenUsage
	if zero.In() != 0 || zero.Total() != 0 || zero.Add(usage) != usage {
		t.Fatalf("zero usage = in %d total %d, adds to %+v; want 0, 0 and the other side", zero.In(), zero.Total(), zero.Add(usage))
	}
}
