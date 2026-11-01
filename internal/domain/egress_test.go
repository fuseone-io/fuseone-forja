package domain_test

import (
	"fmt"
	"testing"
	"testing/quick"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

func denial(i int) domain.EgressDenial {
	return domain.EgressDenial{
		At:          time.Date(2026, 11, 1, 9, 0, i, 0, time.UTC),
		Kind:        domain.EgressConnect,
		Destination: fmt.Sprintf("host-%d.example:443", i),
	}
}

// Whatever the number of denials, the count is exact, the list keeps the
// first MaxEgressDenials in the order they were recorded, and the report
// says it was cut exactly when it was.
func TestEgressReport_record_countExactListBounded(t *testing.T) {
	t.Parallel()
	property := func(n uint16) bool {
		total := int(n % (3 * domain.MaxEgressDenials))
		var r domain.EgressReport
		for i := range total {
			r.Record(denial(i))
		}
		kept := min(total, domain.MaxEgressDenials)
		if r.Count != total || len(r.Denied) != kept || r.Truncated() != (total > kept) {
			t.Logf("n=%d: count %d, kept %d, truncated %v", total, r.Count, len(r.Denied), r.Truncated())
			return false
		}
		for i, d := range r.Denied {
			if d != denial(i) {
				t.Logf("n=%d: Denied[%d] = %+v, want %+v", total, i, d, denial(i))
				return false
			}
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 300}); err != nil {
		t.Fatal(err)
	}
}

func TestEgressReport_boundaries_truncatedOnlyPastTheCap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		n         int
		truncated bool
	}{
		{n: 0, truncated: false},
		{n: domain.MaxEgressDenials, truncated: false},
		{n: domain.MaxEgressDenials + 1, truncated: true},
	}
	for _, tt := range tests {
		var r domain.EgressReport
		for i := range tt.n {
			r.Record(denial(i))
		}
		if r.Count != tt.n || r.Truncated() != tt.truncated {
			t.Errorf("n=%d: count %d, truncated %v; want %d, %v", tt.n, r.Count, r.Truncated(), tt.n, tt.truncated)
		}
	}
}
