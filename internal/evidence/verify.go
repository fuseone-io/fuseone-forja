package evidence

import (
	"fmt"
	"strings"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// VerifyRun is what the evidence of one verify run cites.
type VerifyRun struct {
	RunID string
	// Issue is the one the pull request's branch names, which the run held.
	Issue       int
	PullRequest domain.PullRequest
	BaseBranch  string
	// BaseSHA is the commit of the base branch the contract was read at;
	// ContractSHA the blob of .forja.yaml there, the contract applied.
	BaseSHA     string
	ContractSHA string
	// HeadSHA is the pull request's head, which forja/qa is published on.
	HeadSHA string
	// QA is the verdict and the excluded paths; Reason why a run that
	// reached no verdict stopped.
	QA     domain.QA
	Reason domain.FailureReason
	// Baseline is the base contract's gates on the head before any
	// inversion; empty when they never ran.
	Baseline domain.GateReport
	// Inversions are the candidates the control plane looked at, in order.
	Inversions []domain.Inversion
	// Tokens and Compute are what this verify run consumed, the QA agent's
	// and every gates run's, and nothing of the run that opened the pull
	// request.
	Tokens  domain.TokenUsage
	Compute time.Duration
}

// RenderVerify is the report of a verify run. The same run always renders
// the same body; every path in it is a code span the path cannot close,
// and every patch a fenced block it cannot close.
func RenderVerify(r VerifyRun) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Refs #%d\n\n", r.Issue)
	b.WriteString("Forja classified the pull request's changed paths under the base commit's contract.\n\n")
	table(&b, verifyRows(r))
	b.WriteString("\n## Excluded paths\n\n")
	if len(r.QA.Excluded) == 0 {
		b.WriteString("_None._\n")
	}
	for _, p := range r.QA.Excluded {
		fmt.Fprintf(&b, "- %s\n", code(p))
	}
	b.WriteString("\n## Inversions\n\n")
	if len(r.Inversions) == 0 {
		b.WriteString("_None._\n\n")
	}
	for _, inv := range r.Inversions {
		inversionSection(&b, inv)
	}
	b.WriteString(footer(r.RunID))
	return b.String()
}

func verifyRows(r VerifyRun) [][2]string {
	rows := [][2]string{
		{"Run", code(r.RunID)},
		{"Pull request", fmt.Sprintf("#%d %s", r.PullRequest.Number, cell(r.PullRequest.URL))},
		{"Base branch", code(r.BaseBranch)},
		{"Base commit", code(r.BaseSHA)},
		{"Contract (`.forja.yaml` blob at the base)", code(r.ContractSHA)},
		{"Head", code(r.HeadSHA)},
		{"Verdict", orDash(string(r.QA.Verdict))},
		{"Verdict reason", orDash(string(r.QA.Reason))},
	}
	if r.Reason != "" {
		rows = append(rows, [2]string{"Failure reason", code(string(r.Reason))})
	}
	return append(rows,
		[2]string{"Baseline gates on the head", baseline(r.Baseline)},
		[2]string{"Tokens in / out", fmt.Sprintf("%d / %d", r.Tokens.In(), r.Tokens.Output)},
		[2]string{"Compute minutes", fmt.Sprintf("%.3f", r.Compute.Minutes())},
	)
}

func table(b *strings.Builder, rows [][2]string) {
	b.WriteString("| | |\n|---|---|\n")
	for _, row := range rows {
		fmt.Fprintf(b, "| %s | %s |\n", row[0], row[1])
	}
}

// baseline is how the head's own gates ended: a dash when they never ran.
func baseline(r domain.GateReport) string {
	switch {
	case len(r.Setup)+len(r.Gates) == 0:
		return "—"
	case r.OK():
		return "passed"
	}
	return "failed at " + code(r.FailedStep)
}

// inversionSection is one candidate: what it is, what became of it, the
// paths it changes against the head and its patch.
func inversionSection(b *strings.Builder, inv domain.Inversion) {
	fmt.Fprintf(b, "### %s\n\n", code(inv.Branch))
	table(b, [][2]string{
		{"Commit", orDash(inv.Commit)},
		{"Outcome", outcome(inv)},
		{"Red gate", orDash(inv.Gates.FailedStep)},
	})
	b.WriteString("\nPaths changed against the head:\n\n")
	for _, p := range inv.Paths {
		fmt.Fprintf(b, "- %s\n", code(p))
	}
	b.WriteString("\n")
	if inv.Patch == "" {
		return
	}
	if inv.PatchTruncated {
		b.WriteString("_The patch was cut at its bound._\n\n")
	}
	fence(b, inv.Patch)
}

// outcome is bit or escaped, inconclusive for gates that showed neither,
// not run for an admitted candidate the run stopped before, or the reason
// a candidate was refused.
func outcome(inv domain.Inversion) string {
	switch {
	case !inv.Admitted:
		return "rejected: " + code(string(inv.Rejected))
	case inv.Outcome != "":
		return code(string(inv.Outcome))
	case len(inv.Gates.Setup)+len(inv.Gates.Gates) > 0:
		return code("inconclusive")
	}
	return "not run"
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return code(s)
}
