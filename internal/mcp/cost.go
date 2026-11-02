package mcp

import (
	"math"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// computeMinutes is d in container-minutes rounded to three decimals, the
// unit the wire reports compute in. The estimate never reads it: it prices
// the duration itself, in integers.
func computeMinutes(d time.Duration) float64 {
	return math.Round(float64(d)/float64(time.Minute)*1000) / 1000
}

// estimate is what run cost so far at prices, in USD micros, or nil when
// the server has no price table.
func estimate(prices *domain.PriceTable, run domain.Run) *int64 {
	if prices == nil {
		return nil
	}
	micros := int64(prices.Estimate(run.Compute, run.Tokens))
	return &micros
}
