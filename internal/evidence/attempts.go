package evidence

import (
	"fmt"
	"strings"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// earlierHeading opens the attempts before the last one.
const earlierHeading = "## Earlier attempts\n\n"

// earlier is every attempt but the last, whose head, steps and logs the
// body already shows above.
func earlier(attempts []domain.Attempt) []domain.Attempt {
	if len(attempts) < 2 {
		return nil
	}
	return attempts[:len(attempts)-1]
}

// attemptTables is, for each earlier attempt, its heading, its head, its
// failed step, its tokens and its steps table; the first one opens the
// section. They are always kept whole.
func attemptTables(attempts []domain.Attempt) []string {
	out := make([]string, 0, len(attempts))
	for i, a := range attempts {
		var b strings.Builder
		if i == 0 {
			b.WriteString(earlierHeading)
		}
		fmt.Fprintf(&b, "### Attempt %d\n\n| | |\n|---|---|\n", a.Number)
		writeRows(&b, [][2]string{
			{"Head", code(a.HeadSHA)},
			{"Failed step", failedStep(a.Gates)},
			{"Tokens in / out", tokens(a.Tokens)},
		})
		b.WriteString("\n")
		stepsTable(&b, a.Gates)
		b.WriteString("\n")
		out = append(out, b.String())
	}
	return out
}

// writeAttempts writes each earlier attempt's table and then as many of
// its log blocks as the budget left by the last attempt's logs allows.
func writeAttempts(b *strings.Builder, attempts []domain.Attempt, tables []string, budget *logBudget) {
	for i, a := range attempts {
		b.WriteString(tables[i])
		budget.writeSteps(b, a.Gates)
	}
}

func failedStep(report domain.GateReport) string {
	if report.FailedStep == "" {
		return "—"
	}
	return code(report.FailedStep)
}

func totalLen(parts []string) int {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	return n
}
