package evidence

import (
	"fmt"
	"strings"

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
}

// RenderVerify is the report of a verify run. The same run always renders
// the same body; every path in it is a code span the path cannot close.
func RenderVerify(r VerifyRun) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Refs #%d\n\n", r.Issue)
	b.WriteString("Forja classified the pull request's changed paths under the base commit's contract.\n\n")
	b.WriteString("| | |\n|---|---|\n")
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
	for _, row := range rows {
		fmt.Fprintf(&b, "| %s | %s |\n", row[0], row[1])
	}
	b.WriteString("\n## Excluded paths\n\n")
	if len(r.QA.Excluded) == 0 {
		b.WriteString("_None._\n")
	}
	for _, p := range r.QA.Excluded {
		fmt.Fprintf(&b, "- %s\n", code(p))
	}
	b.WriteString("\n" + footer(r.RunID))
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return code(s)
}
