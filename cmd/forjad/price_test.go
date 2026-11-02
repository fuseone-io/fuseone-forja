package main

import (
	"io"
	"strings"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

var priceVariables = []string{"FORJA_PRICE_INPUT", "FORJA_PRICE_CACHE_WRITE", "FORJA_PRICE_CACHE_READ", "FORJA_PRICE_OUTPUT", "FORJA_PRICE_COMPUTE_MINUTE"}

// priceEnv is a complete price table, each variable a value no other has,
// with overrides applied; an empty override unsets the variable.
func priceEnv(overrides map[string]string) func(string) string {
	env := map[string]string{
		"FORJA_PRICE_INPUT": "1100000", "FORJA_PRICE_CACHE_WRITE": "1200000", "FORJA_PRICE_CACHE_READ": "130000",
		"FORJA_PRICE_OUTPUT": "1400000", "FORJA_PRICE_COMPUTE_MINUTE": "15000",
	}
	for k, v := range overrides {
		env[k] = v
	}
	return func(k string) string { return env[k] }
}

func TestPriceTable_allSet_parsed(t *testing.T) {
	t.Parallel()
	got, err := priceTable(priceEnv(nil))
	want := domain.PriceTable{Input: 1_100_000, CacheWrite: 1_200_000, CacheRead: 130_000, Output: 1_400_000, ComputeMinute: 15_000}
	if err != nil || got == nil || *got != want {
		t.Fatalf("priceTable = %+v, %v; want %+v", got, err, want)
	}
}

// Zero is a price: an operator may give compute away.
func TestPriceTable_zeroPrice_accepted(t *testing.T) {
	t.Parallel()
	got, err := priceTable(priceEnv(map[string]string{"FORJA_PRICE_COMPUTE_MINUTE": "0"}))
	if err != nil || got == nil || got.ComputeMinute != 0 || got.Output != 1_400_000 {
		t.Fatalf("priceTable = %+v, %v; want the table with free compute", got, err)
	}
}

// No price at all is a server that estimates nothing, never one that
// prices everything at zero.
func TestPriceTable_noneSet_nilTable(t *testing.T) {
	t.Parallel()
	got, err := priceTable(func(string) string { return "" })
	if err != nil || got != nil {
		t.Fatalf("priceTable with nothing set = %+v, %v; want no table and no error", got, err)
	}
}

// A table missing one price would estimate that class as free: the server
// does not start, and names what is missing.
func TestPriceTable_partialSet_startupFailureNamesVariable(t *testing.T) {
	t.Parallel()
	for _, missing := range priceVariables {
		t.Run(missing, func(t *testing.T) {
			t.Parallel()
			_, err := priceTable(priceEnv(map[string]string{missing: ""}))
			if err == nil || !strings.Contains(err.Error(), missing) {
				t.Fatalf("priceTable without %s = %v, want an error naming it", missing, err)
			}
		})
	}
}

func TestPriceTable_negativeOrMalformed_refused(t *testing.T) {
	t.Parallel()
	for _, variable := range priceVariables {
		for _, value := range []string{"-1", "lots", "1.5", "1e6", " 7", "9223372036854775808"} {
			t.Run(variable+"="+value, func(t *testing.T) {
				t.Parallel()
				_, err := priceTable(priceEnv(map[string]string{variable: value}))
				if err == nil || !strings.Contains(err.Error(), variable) {
					t.Fatalf("priceTable with %s=%q = %v, want an error naming it", variable, value, err)
				}
			})
		}
	}
}

// The table the environment sets is the one the tools estimate with; a
// partial one stops the server before it serves.
func TestWire_prices_reachTheToolsOrStopTheStart(t *testing.T) {
	t.Parallel()
	deps, _, err := wire(priceEnv(nil), io.Discard)
	want := domain.PriceTable{Input: 1_100_000, CacheWrite: 1_200_000, CacheRead: 130_000, Output: 1_400_000, ComputeMinute: 15_000}
	if err != nil || deps.Prices == nil || *deps.Prices != want {
		t.Fatalf("wire = prices %+v, %v; want %+v", deps.Prices, err, want)
	}
	bare, _, err := wire(func(string) string { return "" }, io.Discard)
	if err != nil || bare.Prices != nil {
		t.Fatalf("wire with no prices = %+v, %v; want none", bare.Prices, err)
	}
	if _, _, err := wire(priceEnv(map[string]string{"FORJA_PRICE_OUTPUT": ""}), io.Discard); err == nil || !strings.Contains(err.Error(), "FORJA_PRICE_OUTPUT") {
		t.Fatalf("wire with a partial table = %v, want a startup error naming FORJA_PRICE_OUTPUT", err)
	}
}
