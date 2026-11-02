package domain

// ChangeKind is what happened to one path between the base commit and the
// agent's head.
type ChangeKind string

// The kinds of change a diff without rename detection reports.
const (
	ChangeAdded       ChangeKind = "added"
	ChangeModified    ChangeKind = "modified"
	ChangeDeleted     ChangeKind = "deleted"
	ChangeTypeChanged ChangeKind = "type_changed"
)

// FileChange is one path the agent's head changed against the base commit,
// with the lines added and deleted; Binary has no line counts.
type FileChange struct {
	Path           string
	Kind           ChangeKind
	Added, Deleted int
	Binary         bool
}

// Proof is what of the agent's change asks for a human merge: the files of
// proof_paths that existed at the base and were changed, and every change
// under human_merge_paths.
type Proof struct {
	ProofPaths      []FileChange
	HumanMergePaths []FileChange
}

// HumanMerge reports whether the pull request needs a human to merge it.
func (p Proof) HumanMerge() bool {
	return len(p.ProofPaths) > 0 || len(p.HumanMergePaths) > 0
}

// ClassifyProof sorts changes by the lists of c, the base commit's
// contract. A proof_paths file counts only when it existed at the base: a
// file the agent added is a new test, not a loosened one. Any change under
// human_merge_paths counts, an addition included. A change may land in
// both lists; each keeps the input order.
func ClassifyProof(c Contract, changes []FileChange) Proof {
	var p Proof
	for _, ch := range changes {
		if ch.Kind != ChangeAdded && matchAny(c.ProofPaths, ch.Path) {
			p.ProofPaths = append(p.ProofPaths, ch)
		}
		if matchAny(c.HumanMergePaths, ch.Path) {
			p.HumanMergePaths = append(p.HumanMergePaths, ch)
		}
	}
	return p
}

func matchAny(patterns []string, name string) bool {
	for _, pattern := range patterns {
		if MatchPath(pattern, name) {
			return true
		}
	}
	return false
}
