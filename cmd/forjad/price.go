package main

import (
	"fmt"
	"strconv"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// The environment variables the price table is configured from, each a
// non-negative integer of USD micros: per million tokens of its class, or
// per container-minute. They are set together or not at all.
const (
	EnvPriceInput         = "FORJA_PRICE_INPUT"
	EnvPriceCacheWrite    = "FORJA_PRICE_CACHE_WRITE"
	EnvPriceCacheRead     = "FORJA_PRICE_CACHE_READ"
	EnvPriceOutput        = "FORJA_PRICE_OUTPUT"
	EnvPriceComputeMinute = "FORJA_PRICE_COMPUTE_MINUTE"
)

// price is one variable of the table and the field it sets.
type price struct {
	name string
	dst  *int64
}

// priceTable is the operator's price table, or nil when no price is set:
// the server then estimates no cost. A table missing a price would bill
// that class as free, so a partial or malformed one is an error naming
// the variable.
func priceTable(getenv func(string) string) (*domain.PriceTable, error) {
	var p domain.PriceTable
	prices := []price{
		{EnvPriceInput, &p.Input}, {EnvPriceCacheWrite, &p.CacheWrite}, {EnvPriceCacheRead, &p.CacheRead},
		{EnvPriceOutput, &p.Output}, {EnvPriceComputeMinute, &p.ComputeMinute},
	}
	if !anySet(getenv, prices) {
		return nil, nil
	}
	for _, pr := range prices {
		v, err := strconv.ParseInt(getenv(pr.name), 10, 64)
		if err != nil || v < 0 {
			return nil, fmt.Errorf("%s: must be a non-negative integer of USD micros, set with every other FORJA_PRICE_ variable", pr.name)
		}
		*pr.dst = v
	}
	return &p, nil
}

func anySet(getenv func(string) string, prices []price) bool {
	for _, pr := range prices {
		if getenv(pr.name) != "" {
			return true
		}
	}
	return false
}
