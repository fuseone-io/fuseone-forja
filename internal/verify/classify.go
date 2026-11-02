// Package verify decides what forja.verify concludes about a pull request,
// from the base commit's contract and the paths the pull request changes.
// It does no I/O.
package verify

import (
	"sort"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// Outcome is what a diff is under a contract. Production means the diff
// changes a path outside verify.exclude and proof_paths, which only an
// inversion can verify: QA then carries no verdict. Otherwise QA is not
// verifiable with its reason. QA.Excluded is set either way.
type Outcome struct {
	Production bool
	QA         domain.QA
}

// Classify folds the changed paths over the contract. A rename counts both
// its paths, so a file moved out of production code is still production
// code. A path that verify.exclude matches is excluded even when a proof
// path matches it too.
func Classify(c domain.Contract, files []domain.ChangedFile) Outcome {
	excluded := map[string]bool{}
	var proof, production bool
	for _, p := range changedPaths(files) {
		switch kindOf(c, p) {
		case pathExcluded:
			excluded[p] = true
		case pathProof:
			proof = true
		default:
			production = true
		}
	}
	qa := domain.QA{Excluded: sorted(excluded)}
	if production {
		return Outcome{Production: true, QA: qa}
	}
	qa.Verdict, qa.Reason = domain.VerdictNotVerifiable, domain.NotVerifiableNoProductionCode
	if len(excluded) > 0 && !proof {
		qa.Reason = domain.NotVerifiableOnlyExcludedPaths
	}
	return Outcome{QA: qa}
}

// pathKind is what one path is under a contract.
type pathKind int

const (
	pathProduction pathKind = iota
	pathExcluded
	pathProof
)

// kindOf is p under c: excluded when verify.exclude matches it, even when
// a proof path matches it too, then a proof path, then production code.
func kindOf(c domain.Contract, p string) pathKind {
	switch {
	case matches(c.Verify.Exclude, p):
		return pathExcluded
	case matches(c.ProofPaths, p):
		return pathProof
	}
	return pathProduction
}

// changedPaths is every path the files name, the previous path of a
// rename included.
func changedPaths(files []domain.ChangedFile) []string {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
		if f.PreviousPath != "" {
			paths = append(paths, f.PreviousPath)
		}
	}
	return paths
}

func matches(patterns []string, p string) bool {
	_, ok := domain.MatchPath(patterns, p)
	return ok
}

func sorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
