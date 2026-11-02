package domain

// Verdict is what forja.verify concludes about a pull request.
type Verdict string

// The three verdicts. Bites publishes forja/qa green, DoesNotBite red,
// NotVerifiable a status that is never green.
const (
	VerdictBites         Verdict = "bites"
	VerdictDoesNotBite   Verdict = "does_not_bite"
	VerdictNotVerifiable Verdict = "not_verifiable"
)

// NotVerifiableReason says why a pull request could not be verified.
type NotVerifiableReason string

// The reasons a diff is not verifiable.
const (
	// NotVerifiableOnlyExcludedPaths is a diff whose every path matches
	// verify.exclude.
	NotVerifiableOnlyExcludedPaths NotVerifiableReason = "only_excluded_paths"
	// NotVerifiableNoProductionCode is a diff with no path outside
	// verify.exclude and proof_paths, or no path at all.
	NotVerifiableNoProductionCode NotVerifiableReason = "no_production_code"
)

// QA is the verdict of a verify run. The zero value is no verdict yet.
// Excluded is every changed path that matched verify.exclude, sorted and
// without repeats, whatever the verdict.
type QA struct {
	Verdict  Verdict
	Reason   NotVerifiableReason
	Excluded []string
}

// ChangedFile is one file a pull request changes. PreviousPath is the
// path it had before a rename; empty otherwise.
type ChangedFile struct {
	Path         string
	PreviousPath string
}

// PullRequestDiff is a pull request as forja.verify reads it: where its
// head is, which branch it targets, and every file it changes. HeadRepository
// is empty when the head's repository no longer exists.
type PullRequestDiff struct {
	Number         int
	URL            string
	HeadRepository string
	HeadRef        string
	HeadSHA        string
	BaseRef        string
	Files          []ChangedFile
}
