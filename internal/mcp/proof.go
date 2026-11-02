package mcp

import "github.com/fuseone-io/fuseone-forja/internal/domain"

// ChangedPath is one path of the agent's head, against the base commit,
// that asks for a human merge. Change is added, modified, deleted or
// type_changed; a binary file has no line counts.
type ChangedPath struct {
	Path    string `json:"path"`
	Change  string `json:"change"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	Binary  bool   `json:"binary"`
}

// changedPaths is the wire form of changes, empty rather than nil.
func changedPaths(changes []domain.FileChange) []ChangedPath {
	out := make([]ChangedPath, 0, len(changes))
	for _, c := range changes {
		out = append(out, ChangedPath{Path: c.Path, Change: string(c.Kind), Added: c.Added, Deleted: c.Deleted, Binary: c.Binary})
	}
	return out
}
