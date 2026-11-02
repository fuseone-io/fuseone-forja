package verify

import "github.com/fuseone-io/fuseone-forja/internal/domain"

// Admit decides whether a QA candidate may run as an inversion of a pull
// request that changes pr, under the base contract c. inv is what the
// candidate changes against the pull request's head, fromBase what it
// changes against the base. A candidate is refused when it changes
// nothing; when it touches a path that is not production code, which
// keeps every test and testdata in place; when it touches a production
// path the pull request does not change; and when no production path of
// the pull request is left changed against the base, which is reverting
// the whole change.
func Admit(c domain.Contract, pr []domain.ChangedFile, inv domain.Change, fromBase domain.Change) (bool, domain.RejectReason) {
	if len(inv.Paths) == 0 {
		return false, domain.RejectEmpty
	}
	for _, p := range inv.Paths {
		if kindOf(c, p) != pathProduction {
			return false, domain.RejectTouchesNonProduction
		}
	}
	production := set(ProductionPaths(c, pr))
	for _, p := range inv.Paths {
		if !production[p] {
			return false, domain.RejectOutsideTheChange
		}
	}
	for _, p := range fromBase.Paths {
		if production[p] {
			return true, ""
		}
	}
	return false, domain.RejectRevertsWholeDiff
}

// ProductionPaths is every path of files that is production code under c,
// both sides of a rename, sorted and without repeats; empty, never nil,
// when there is none.
func ProductionPaths(c domain.Contract, files []domain.ChangedFile) []string {
	production := map[string]bool{}
	for _, p := range changedPaths(files) {
		if kindOf(c, p) == pathProduction {
			production[p] = true
		}
	}
	return sorted(production)
}

func set(paths []string) map[string]bool {
	out := make(map[string]bool, len(paths))
	for _, p := range paths {
		out[p] = true
	}
	return out
}
