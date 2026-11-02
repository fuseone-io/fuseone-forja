package evidence

import (
	"fmt"
	"strings"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

const (
	// MaxProofEntries bounds the rows of each list of changed paths.
	MaxProofEntries = 100
	// maxProofBytes bounds each list's table, so that the header, which
	// truncation never trims, always fits under MaxBody whatever the agent
	// named its files.
	maxProofBytes = 12000
)

// humanMergeLine is the marking under the issue reference, empty when the
// pull request needs no human merge.
func humanMergeLine(p domain.Proof) string {
	const lead = "**Human merge required.** The agent changed "
	switch proof, human := len(p.ProofPaths) > 0, len(p.HumanMergePaths) > 0; {
	case proof && human:
		return lead + "existing files of the base contract's `proof_paths` and files under its `human_merge_paths`.\n\n"
	case proof:
		return lead + "existing files of the base contract's `proof_paths`.\n\n"
	case human:
		return lead + "files under the base contract's `human_merge_paths`.\n\n"
	}
	return ""
}

// proofSections are the two lists of changed paths, each only when it has
// an entry.
func proofSections(b *strings.Builder, p domain.Proof, runID string) {
	if len(p.ProofPaths) > 0 {
		b.WriteString("\n## Proof apparatus\n\nFiles of the base contract's `proof_paths` that existed at the base commit and the agent changed.\n\n")
		changeTable(b, p.ProofPaths, runID)
	}
	if len(p.HumanMergePaths) > 0 {
		b.WriteString("\n## Human merge paths\n\nFiles under the base contract's `human_merge_paths` that the agent changed.\n\n")
		changeTable(b, p.HumanMergePaths, runID)
	}
}

// changeTable writes the changes as rows until either bound is reached,
// then says how many it left out and where the full list is.
func changeTable(b *strings.Builder, changes []domain.FileChange, runID string) {
	var t strings.Builder
	t.WriteString("| File | Change | Lines |\n|---|---|---|\n")
	shown := 0
	for _, c := range changes {
		row := fmt.Sprintf("| %s | %s | %s |\n", code(strings.ToValidUTF8(c.Path, "�")), c.Kind, lines(c))
		if shown == MaxProofEntries || t.Len()+len(row) > maxProofBytes {
			break
		}
		t.WriteString(row)
		shown++
	}
	b.WriteString(t.String())
	if left := len(changes) - shown; left > 0 {
		fmt.Fprintf(b, "\n_…and %d more; full list: `forja.status run_id=%s`._\n", left, runID)
	}
}

func lines(c domain.FileChange) string {
	if c.Binary {
		return "binary"
	}
	return fmt.Sprintf("+%d -%d", c.Added, c.Deleted)
}
