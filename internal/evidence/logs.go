package evidence

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// logBudget writes log blocks while they fit in room. The first one that
// does not is replaced by the truncation note, and none is written after.
type logBudget struct {
	room int
	full bool
}

func (l *logBudget) writeSteps(b *strings.Builder, report domain.GateReport) {
	for _, s := range steps(report) {
		l.write(b, logBlock(s))
	}
}

func (l *logBudget) write(b *strings.Builder, block string) {
	if l.full {
		return
	}
	if len(block) > l.room {
		b.WriteString(truncationNote + "\n\n")
		l.full = true
		return
	}
	b.WriteString(block)
	l.room -= len(block)
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
