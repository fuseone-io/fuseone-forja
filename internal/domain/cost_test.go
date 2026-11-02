package domain_test

import (
	"math/big"
	"testing"
	"testing/quick"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// prices gives each class a price no other class has, and a token price
// that is not a whole number of micros per token, so a class priced with
// another's price, or a floor taken per term, gives a value of its own.
var prices = domain.PriceTable{Input: 2_500_000, CacheWrite: 3_000_000, CacheRead: 500_000, Output: 7_000_000, ComputeMinute: 12_000}

func TestPriceTable_estimate_tokensAndComputeInMicros(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		compute time.Duration
		tokens  domain.TokenUsage
		want    domain.Cost
	}{
		{name: "input", tokens: domain.TokenUsage{Input: 1_000_000}, want: 2_500_000},
		{name: "cache write", tokens: domain.TokenUsage{CacheCreation: 1_000_000}, want: 3_000_000},
		{name: "cache read", tokens: domain.TokenUsage{CacheRead: 1_000_000}, want: 500_000},
		{name: "output", tokens: domain.TokenUsage{Output: 1_000_000}, want: 7_000_000},
		{name: "every class", tokens: domain.TokenUsage{Input: 2_000_000, CacheCreation: 3_000_000, CacheRead: 5_000_000, Output: 7_000_000}, want: 5_000_000 + 9_000_000 + 2_500_000 + 49_000_000},
		{name: "tokens summed before the floor", tokens: domain.TokenUsage{Input: 1, CacheRead: 1}, want: 3},
		{name: "tokens floored", tokens: domain.TokenUsage{CacheRead: 3}, want: 1},
		{name: "a minute and a half", compute: 90 * time.Second, want: 18_000},
		{name: "compute floored, not rounded", compute: 59_999 * time.Millisecond, want: 11_999},
		{name: "compute below a micro", compute: time.Millisecond, want: 0},
		{name: "compute a float64 product floors to 26", compute: 135 * time.Millisecond, want: 27},
		{name: "tokens plus compute", compute: 90 * time.Second, tokens: domain.TokenUsage{Output: 1_000_000}, want: 7_018_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := prices.Estimate(tt.compute, tt.tokens); got != tt.want {
				t.Fatalf("Estimate(%v, %+v) = %d, want %d", tt.compute, tt.tokens, got, tt.want)
			}
		})
	}
}

func TestPriceTable_zeroUsage_zeroCost(t *testing.T) {
	t.Parallel()
	if got := prices.Estimate(0, domain.TokenUsage{}); got != 0 {
		t.Fatalf("Estimate of nothing = %d, want 0", got)
	}
}

// bounded keeps a generated count or price where the four products and
// their sum stay inside int64: counts under 2^32, prices under 10^8 micros.
func bounded(n uint32) int64 { return int64(n) }

func boundedPrice(n uint32) int64 { return int64(n % 100_000_000) }

func table(p [5]uint32) domain.PriceTable {
	return domain.PriceTable{
		Input: boundedPrice(p[0]), CacheWrite: boundedPrice(p[1]), CacheRead: boundedPrice(p[2]),
		Output: boundedPrice(p[3]), ComputeMinute: boundedPrice(p[4]),
	}
}

func usageOf(u [4]uint32) domain.TokenUsage {
	return domain.TokenUsage{Input: bounded(u[0]), CacheCreation: bounded(u[1]), CacheRead: bounded(u[2]), Output: bounded(u[3])}
}

// More of any one token class, or more compute, never costs less.
func TestPriceTable_estimate_monotoneInEveryClassAndCompute(t *testing.T) {
	t.Parallel()
	property := func(p [5]uint32, u [4]uint32, ms uint32, class uint8, more uint32) bool {
		pt, base, compute := table(p), usageOf(u), time.Duration(ms)*time.Millisecond
		grown, longer := base, compute
		switch class % 5 {
		case 0:
			grown.Input += bounded(more)
		case 1:
			grown.CacheCreation += bounded(more)
		case 2:
			grown.CacheRead += bounded(more)
		case 3:
			grown.Output += bounded(more)
		default:
			longer += time.Duration(more) * time.Millisecond
		}
		return pt.Estimate(longer, grown) >= pt.Estimate(compute, base)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 1000}); err != nil {
		t.Fatal(err)
	}
}

// The estimate is the formula in exact integers at magnitudes where a
// float64 would already have lost the last micros.
func TestPriceTable_estimate_exactAgainstArbitraryPrecision(t *testing.T) {
	t.Parallel()
	property := func(p [5]uint32, u [4]uint32, ms uint32) bool {
		pt, tokens := table(p), usageOf(u)
		sum := new(big.Int)
		for _, term := range [][2]int64{{tokens.Input, pt.Input}, {tokens.CacheCreation, pt.CacheWrite}, {tokens.CacheRead, pt.CacheRead}, {tokens.Output, pt.Output}} {
			sum.Add(sum, new(big.Int).Mul(big.NewInt(term[0]), big.NewInt(term[1])))
		}
		want := sum.Quo(sum, big.NewInt(1_000_000))
		compute := new(big.Int).Mul(big.NewInt(int64(ms)), big.NewInt(pt.ComputeMinute))
		want.Add(want, compute.Quo(compute, big.NewInt(60_000)))
		return big.NewInt(int64(pt.Estimate(time.Duration(ms)*time.Millisecond, tokens))).Cmp(want) == 0
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 1000}); err != nil {
		t.Fatal(err)
	}
}
