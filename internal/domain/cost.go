package domain

import "time"

// Cost is an amount of money in USD micros, millionths of a dollar.
type Cost int64

// PriceTable is what the operator prices a run at: each token class in USD
// micros per million tokens, and compute in USD micros per container-minute.
// Every price is non-negative.
type PriceTable struct {
	Input         int64
	CacheWrite    int64
	CacheRead     int64
	Output        int64
	ComputeMinute int64
}

// Estimate is what compute and the tokens t cost at p: the four token
// classes priced and summed before one floor to whole micros, plus the
// compute in whole milliseconds priced per minute, floored on its own. The
// products stay inside int64 for counts under 2^32 at prices under 10^8.
func (p PriceTable) Estimate(compute time.Duration, t TokenUsage) Cost {
	tokens := (t.Input*p.Input + t.CacheCreation*p.CacheWrite + t.CacheRead*p.CacheRead + t.Output*p.Output) / 1_000_000
	minutes := compute.Milliseconds() * p.ComputeMinute / 60_000
	return Cost(tokens + minutes)
}
