package agent

import "github.com/fuseone-io/fuseone-forja/internal/domain"

// File is one agent_context file, read from the base commit.
type File struct {
	Path    string
	Content string
}

// Task is one agent run: the contract and the context come from the base
// commit, the tree is the checkout the agent commits on, on Branch,
// started at BaseSHA. Bundle is the path on the control plane the agent's
// commits are exported to. Spent is what the run's earlier attempts used
// of the model; Retry is nil on the first attempt.
type Task struct {
	RunID    string
	Contract domain.Contract
	Tree     string
	Issue    domain.Issue
	Context  []File
	Branch   string
	BaseSHA  string
	Bundle   string
	Spent    domain.TokenUsage
	Retry    *Retry
}

// Retry is what a later attempt is told of the one before: its number out
// of the limit, and the step that did not pass on the previous head, as
// the gates runner kept it.
type Retry struct {
	Attempt     int
	MaxAttempts int
	Failed      domain.GateResult
}
