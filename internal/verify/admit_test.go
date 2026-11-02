package verify_test

import (
	"reflect"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/verify"
)

// coveredPull is a pull request that changes two production files, adds a
// test and its testdata, and documents itself, as a covered pilot change.
func coveredPull() []domain.ChangedFile {
	return []domain.ChangedFile{
		{Path: "internal/domain/cost.go"},
		{Path: "internal/mcp/cost.go"},
		{Path: "internal/domain/cost_test.go"},
		{Path: "internal/domain/testdata/prices.json"},
		{Path: "docs/adr/0045-run-cost.md"},
		{Path: "internal/mcp/estimate.go", PreviousPath: "internal/mcp/price.go"},
	}
}

func change(paths ...string) domain.Change {
	return domain.Change{Paths: paths, Patch: "diff --git a/x b/x\n"}
}

// The base side of an admitted candidate still differs from the base on a
// production path of the pull request, beside its new test.
var stillChanged = change("internal/mcp/cost.go", "internal/domain/cost_test.go")

func TestAdmit_eachRejection_reasonNamed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		inv      domain.Change
		fromBase domain.Change
		want     domain.RejectReason
	}{
		{name: "a test", inv: change("internal/domain/cost.go", "internal/domain/cost_test.go"), fromBase: stillChanged, want: domain.RejectTouchesNonProduction},
		{name: "testdata", inv: change("internal/domain/testdata/prices.json"), fromBase: stillChanged, want: domain.RejectTouchesNonProduction},
		{name: "an excluded path", inv: change("docs/adr/0045-run-cost.md"), fromBase: stillChanged, want: domain.RejectTouchesNonProduction},
		{name: "a test the pull request does not change", inv: change("internal/gates/runner_test.go"), fromBase: stillChanged, want: domain.RejectTouchesNonProduction},
		{name: "a production path outside the pull request", inv: change("internal/gates/runner.go"), fromBase: stillChanged, want: domain.RejectOutsideTheChange},
		{name: "outside and a test, the test named", inv: change("internal/gates/runner.go", "internal/domain/cost_test.go"), fromBase: stillChanged, want: domain.RejectTouchesNonProduction},
		{name: "empty change", inv: domain.Change{}, fromBase: stillChanged, want: domain.RejectEmpty},
		{name: "whole diff reverted", inv: change("internal/domain/cost.go"), fromBase: domain.Change{}, want: domain.RejectRevertsWholeDiff},
		{name: "every production path reverted, the tests left", inv: change("internal/domain/cost.go"), fromBase: change("internal/domain/cost_test.go", "internal/domain/testdata/prices.json", "docs/adr/0045-run-cost.md"), want: domain.RejectRevertsWholeDiff},
		{name: "only a path the base moved left", inv: change("internal/domain/cost.go"), fromBase: change("internal/gates/runner.go"), want: domain.RejectRevertsWholeDiff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ok, reason := verify.Admit(pilotContract(), coveredPull(), tt.inv, tt.fromBase)
			if ok || reason != tt.want {
				t.Fatalf("Admit = %v, %q; want rejected %q", ok, reason, tt.want)
			}
		})
	}
}

// Half of the change, on production code only, with something of the
// change left against the base, is an inversion; so is either side of a
// rename.
func TestAdmit_halfOfTheProductionChange_admitted(t *testing.T) {
	t.Parallel()
	tests := map[string]domain.Change{
		"one of two files":       change("internal/domain/cost.go"),
		"both files":             change("internal/domain/cost.go", "internal/mcp/cost.go"),
		"the new side of rename": change("internal/mcp/estimate.go"),
		"the old side of rename": change("internal/mcp/price.go"),
	}
	for name, inv := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ok, reason := verify.Admit(pilotContract(), coveredPull(), inv, stillChanged)
			if !ok || reason != "" {
				t.Fatalf("Admit = %v, %q; want admitted", ok, reason)
			}
		})
	}
}

// A path both verify.exclude and proof_paths would match, and a path only
// a proof path matches, are refused the same way: the fold is Classify's.
func TestAdmit_samePrecedenceAsClassify(t *testing.T) {
	t.Parallel()
	c := pilotContract()
	pr := files("internal/x.go", "docs/testdata/a.md", "pkg/testdata/a.json")
	for _, p := range []string{"docs/testdata/a.md", "pkg/testdata/a.json"} {
		if verify.Classify(c, files(p)).Production {
			t.Fatalf("Classify says %s is production", p)
		}
		if ok, reason := verify.Admit(c, pr, change(p), change("internal/x.go")); ok || reason != domain.RejectTouchesNonProduction {
			t.Fatalf("Admit(%s) = %v, %q; want touches_non_production", p, ok, reason)
		}
	}
}

// The production paths are what an inversion may touch: every path of
// the pull request Classify counts as production, rename sides included,
// sorted and without repeats.
func TestProductionPaths_onlyProductionSortedBothRenameSides(t *testing.T) {
	t.Parallel()
	pr := append(coveredPull(), domain.ChangedFile{Path: "internal/domain/cost.go"})
	want := []string{"internal/domain/cost.go", "internal/mcp/cost.go", "internal/mcp/estimate.go", "internal/mcp/price.go"}
	if got := verify.ProductionPaths(pilotContract(), pr); !reflect.DeepEqual(got, want) {
		t.Fatalf("ProductionPaths = %q, want %q", got, want)
	}
	if got := verify.ProductionPaths(pilotContract(), files("README.md")); got == nil || len(got) != 0 {
		t.Fatalf("ProductionPaths of markdown = %#v, want empty and not nil", got)
	}
}
