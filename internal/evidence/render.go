// Package evidence renders the report of an implement run as the body of
// its pull request: what the run was, what it ran on, every step's
// command, status, exit code and duration, and each step's raw stream
// tails. It renders what the control plane recorded and never reads the
// streams for a verdict. It does no I/O.
package evidence

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

const (
	// MaxBody bounds the whole body in bytes, below the 65 536 characters
	// a pull request body may hold.
	MaxBody = 60000
	// TailBytes bounds each stream shown.
	TailBytes = 2000
)

// truncationNote replaces the logs that did not fit.
const truncationNote = "_The logs below were truncated to stay under the pull request body limit._"

// Run is what the evidence of one implement run cites.
type Run struct {
	Issue      int
	RunID      string
	BaseBranch string
	// BaseSHA is the commit the run started from; ContractSHA the blob of
	// .forja.yaml there, the contract the gates ran under.
	BaseSHA     string
	ContractSHA string
	// HeadSHA is the agent's commit the gates ran on, and Gates their
	// report, both of the last attempt.
	HeadSHA string
	Gates   domain.GateReport
	Tokens  domain.TokenUsage
	// Elapsed is the run's wall time so far, from the control plane's clock.
	Elapsed time.Duration
	// Attempts are every attempt whose gates ran, the last one included;
	// MaxAttempts is the contract's limit.
	Attempts    []domain.Attempt
	MaxAttempts int
}

// Render is the pull request body for r. The same Run always renders the
// same body, and the body never exceeds MaxBody: logs that do not fit are
// left out with a note, the earlier attempts' before the last one's, and
// the footer naming where the full tails are is always kept.
func Render(r Run) string {
	head := header(r)
	foot := footer(r.RunID)
	prior := earlier(r.Attempts)
	tables := attemptTables(prior)
	budget := logBudget{room: MaxBody - len(head) - len(foot) - len(truncationNote) - 2 - totalLen(tables)}
	var b strings.Builder
	b.WriteString(head)
	budget.writeSteps(&b, r.Gates)
	writeAttempts(&b, prior, tables, &budget)
	b.WriteString(foot)
	return cut(b.String(), foot)
}

func steps(report domain.GateReport) []domain.GateResult {
	return append(append([]domain.GateResult(nil), report.Setup...), report.Gates...)
}

// header is the reference, the identifiers and the table of steps.
func header(r Run) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Refs #%d\n\n", r.Issue)
	b.WriteString("Forja ran the gates of the base commit's contract on the agent's head.\n\n")
	b.WriteString("| | |\n|---|---|\n")
	rows := [][2]string{
		{"Run", code(r.RunID)},
		{"Base branch", code(r.BaseBranch)},
		{"Base commit", code(r.BaseSHA)},
		{"Contract (`.forja.yaml` blob at the base)", code(r.ContractSHA)},
		{"Head", code(r.HeadSHA)},
		{"Tokens in / out", tokens(r.Tokens)},
		{"Attempts", fmt.Sprintf("%d of %d", len(r.Attempts), r.MaxAttempts)},
		{"Elapsed", r.Elapsed.Round(time.Second).String()},
	}
	writeRows(&b, rows)
	b.WriteString("\n## Steps\n\n")
	stepsTable(&b, r.Gates)
	b.WriteString("\n## Logs\n\n")
	return b.String()
}

func writeRows(b *strings.Builder, rows [][2]string) {
	for _, row := range rows {
		fmt.Fprintf(b, "| %s | %s |\n", row[0], row[1])
	}
}

// stepsTable is every step of report with its command, status, exit code
// and duration.
func stepsTable(b *strings.Builder, report domain.GateReport) {
	b.WriteString("| Step | Command | Status | Exit code | Duration |\n|---|---|---|---|---|\n")
	for _, s := range steps(report) {
		fmt.Fprintf(b, "| %s | %s | %s | %s | %s |\n", cell(s.Name), code(s.Command), s.Status, exitCode(s), duration(s))
	}
}

func tokens(u domain.TokenUsage) string {
	return fmt.Sprintf("%d / %d", u.In(), u.Output)
}

func footer(runID string) string {
	return "Full tail: `forja.status run_id=" + runID + "`\n"
}

// code is s as an inline code span that a backtick inside cannot close,
// fit for a table cell.
func code(s string) string {
	s = cell(s)
	marks := strings.Repeat("`", longestRun(s)+1)
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		s = " " + s + " "
	}
	return marks + s + marks
}

// cell keeps s on one line of a table cell.
func cell(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.ReplaceAll(s, "|", `\|`)
}

func exitCode(s domain.GateResult) string {
	if s.ExitCode == domain.NoExitCode {
		return "—"
	}
	return fmt.Sprint(s.ExitCode)
}

func duration(s domain.GateResult) string {
	if s.Status == domain.StepSkipped {
		return "—"
	}
	return s.Duration.Round(time.Millisecond).String()
}

func longestRun(s string) int {
	longest, run := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] != '`' {
			run = 0
			continue
		}
		run++
		longest = max(longest, run)
	}
	return longest
}

// cut keeps body under MaxBody when even the header does not fit: the
// head is cut on a character, and the note and the footer close it.
func cut(body, foot string) string {
	if len(body) <= MaxBody {
		return body
	}
	keep := MaxBody - len(foot) - len(truncationNote) - 2
	for keep > 0 && !utf8.RuneStart(body[keep]) {
		keep--
	}
	return body[:keep] + "\n" + truncationNote + "\n" + foot
}
