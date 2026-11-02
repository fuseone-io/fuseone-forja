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
	// HeadSHA is the agent's commit the gates ran on.
	HeadSHA string
	Gates   domain.GateReport
	Tokens  domain.TokenUsage
	// Elapsed is the run's wall time so far, from the control plane's clock.
	Elapsed time.Duration
	// Limits are the limits the run worked under; DiffLines the lines the
	// head adds and deletes against the base, as the control plane counted.
	Limits    domain.Limits
	DiffLines int
}

// Render is the pull request body for r. The same Run always renders the
// same body, and the body never exceeds MaxBody: logs that do not fit are
// left out with a note, and the footer naming where the full tails are is
// always kept.
func Render(r Run) string {
	head := header(r)
	foot := footer(r.RunID)
	steps := append(append([]domain.GateResult(nil), r.Gates.Setup...), r.Gates.Gates...)
	var logs strings.Builder
	room := MaxBody - len(head) - len(foot) - len(truncationNote) - 2
	for _, s := range steps {
		block := logBlock(s)
		if logs.Len()+len(block) > room {
			logs.WriteString(truncationNote + "\n\n")
			break
		}
		logs.WriteString(block)
	}
	return cut(head+logs.String()+foot, foot)
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
		{"Tokens in / out", fmt.Sprintf("%d / %d", r.Tokens.In(), r.Tokens.Output)},
		{"Elapsed", r.Elapsed.Round(time.Second).String()},
		{"Wall clock limit", r.Limits.WallClock.String()},
		{"Diff lines (measured / limit)", fmt.Sprintf("%d / %d", r.DiffLines, r.Limits.MaxDiffLines)},
	}
	for _, row := range rows {
		fmt.Fprintf(&b, "| %s | %s |\n", row[0], row[1])
	}
	b.WriteString("\n## Steps\n\n| Step | Command | Status | Exit code | Duration |\n|---|---|---|---|---|\n")
	for _, s := range append(append([]domain.GateResult(nil), r.Gates.Setup...), r.Gates.Gates...) {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", cell(s.Name), code(s.Command), s.Status, exitCode(s), duration(s))
	}
	b.WriteString("\n## Logs\n\n")
	return b.String()
}

func footer(runID string) string {
	return "Full tail: `forja.status run_id=" + runID + "`\n"
}

// logBlock is one step's streams, each by its tail, in a collapsed block.
func logBlock(s domain.GateResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<details><summary>%s: %s, exit %s</summary>\n\n", cell(s.Name), s.Status, exitCode(s))
	if s.Reason != "" {
		fmt.Fprintf(&b, "Reason: %s\n\n", cell(s.Reason))
	}
	for _, stream := range []struct {
		name string
		data []byte
	}{{"stdout", s.Stdout}, {"stderr", s.Stderr}} {
		if len(stream.data) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s, last %d bytes at most:\n\n", stream.name, TailBytes)
		fence(&b, tail(stream.data))
	}
	b.WriteString("</details>\n\n")
	return b.String()
}

// tail is the last TailBytes of data at most, starting on a character.
func tail(data []byte) string {
	if len(data) <= TailBytes {
		return strings.ToValidUTF8(string(data), "�")
	}
	start := len(data) - TailBytes
	for start < len(data) && !utf8.RuneStart(data[start]) {
		start++
	}
	return strings.ToValidUTF8(string(data[start:]), "�")
}

// fence writes text in a fenced block whose fence is longer than any run
// of backticks inside it, so the text cannot close it.
func fence(b *strings.Builder, text string) {
	marks := strings.Repeat("`", max(3, longestRun(text)+1))
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	fmt.Fprintf(b, "%s\n%s%s\n\n", marks, text, marks)
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
