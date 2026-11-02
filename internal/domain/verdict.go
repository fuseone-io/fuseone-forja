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
// without repeats, whatever the verdict. Escaped is the branch of the first
// inversion that passed every gate, for a verdict that does not bite.
type QA struct {
	Verdict  Verdict
	Reason   NotVerifiableReason
	Excluded []string
	Escaped  string
}

// RejectReason says why the control plane refused a QA candidate as an
// inversion.
type RejectReason string

// The reasons a candidate is not admitted.
const (
	// RejectEmpty is a candidate that changes nothing against the head.
	RejectEmpty RejectReason = "empty"
	// RejectTouchesNonProduction is a candidate that changes an excluded
	// path or a proof path of the base contract, such as a test.
	RejectTouchesNonProduction RejectReason = "touches_non_production"
	// RejectOutsideTheChange is a candidate that changes a production path
	// the pull request does not change.
	RejectOutsideTheChange RejectReason = "outside_the_change"
	// RejectRevertsWholeDiff is a candidate that leaves no production path
	// of the pull request changed against the base.
	RejectRevertsWholeDiff RejectReason = "reverts_whole_diff"
)

// InversionOutcome is what the base contract's gates said of one inversion.
type InversionOutcome string

// An inversion bit when a gate that ran exited non-zero, and escaped when
// every step passed.
const (
	InversionBit     InversionOutcome = "bit"
	InversionEscaped InversionOutcome = "escaped"
)

// Change is what one commit changes against another: every path, a
// rename as both of its paths, and the patch, cut at a bound that
// Truncated reports.
type Change struct {
	Paths     []string
	Patch     string
	Truncated bool
}

// Inversion is one candidate the QA agent proposed on Branch. Commit,
// Paths and Patch are what it changes against the pull request's head.
// A candidate is either Admitted, and then its Gates ran and Outcome says
// what they showed, empty when they showed neither, or Rejected.
type Inversion struct {
	Branch         string
	Commit         string
	Paths          []string
	Patch          string
	PatchTruncated bool
	Admitted       bool
	Rejected       RejectReason
	Gates          GateReport
	Outcome        InversionOutcome
}

// ChangedFile is one file a pull request changes. PreviousPath is the
// path it had before a rename; empty otherwise.
type ChangedFile struct {
	Path         string
	PreviousPath string
}

// PullRequestDiff is a pull request as forja.verify reads it: where its
// head is, which branch it targets, every file it changes and its body,
// which is untrusted text. HeadRepository is empty when the head's
// repository no longer exists.
type PullRequestDiff struct {
	Number         int
	URL            string
	HeadRepository string
	HeadRef        string
	HeadSHA        string
	BaseRef        string
	Body           string
	Files          []ChangedFile
}
