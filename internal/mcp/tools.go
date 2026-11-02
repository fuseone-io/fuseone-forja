package mcp

import (
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// ImplementInput is the argument of forja.implement.
type ImplementInput struct {
	Repository string `json:"repository" jsonschema:"repository in owner/name form"`
	Issue      int    `json:"issue" jsonschema:"issue number to implement"`
	BaseBranch string `json:"base_branch" jsonschema:"branch the PR targets"`
}

// ImplementOutput is the result of forja.implement: how the run ended,
// what it ran on and, when its gates were green, the pull request it
// opened. A red gate is a result, not a tool error. Setup and Gates are
// always on the wire, empty when no gate ran.
type ImplementOutput struct {
	RunID string `json:"run_id"`
	State string `json:"state"`
	// FailureReason and FailedStep are absent unless the run failed for a
	// reason the control plane names, or at a step of its gates.
	FailureReason string `json:"failure_reason,omitempty"`
	FailedStep    string `json:"failed_step,omitempty"`
	// BaseSHA is the base commit, ContractSHA the blob of .forja.yaml there,
	// HeadSHA the agent's commit the gates ran on, Branch the run's branch.
	BaseSHA     string `json:"base_sha,omitempty"`
	ContractSHA string `json:"contract_sha,omitempty"`
	HeadSHA     string `json:"head_sha,omitempty"`
	Branch      string `json:"branch,omitempty"`
	// PullRequest is absent when the run opened none.
	PullRequest *PullRequestRef `json:"pull_request,omitempty"`
	// Evidence is the report the pull request carries, rendered once the
	// gates ran; absent before.
	Evidence  string       `json:"evidence,omitempty"`
	TokensIn  int64        `json:"tokens_in"`
	TokensOut int64        `json:"tokens_out"`
	Setup     []StepReport `json:"setup"`
	Gates     []StepReport `json:"gates"`
}

// PullRequestRef is a pull request a run opened.
type PullRequestRef struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

// VerifyInput is the argument of forja.verify.
type VerifyInput struct {
	Repository  string `json:"repository" jsonschema:"repository in owner/name form"`
	PullRequest int    `json:"pull_request" jsonschema:"pull request number to verify"`
}

// VerifyOutput is the result of forja.verify: how the run ended, what the
// verdict was reached on, and the verdict. A diff that only an inversion
// can verify is a result, not a tool error. Excluded is always on the
// wire, empty when no changed path matched verify.exclude.
type VerifyOutput struct {
	RunID string `json:"run_id"`
	State string `json:"state"`
	// Verdict and VerdictReason are absent until the run reached a verdict.
	Verdict       string   `json:"verdict,omitempty"`
	VerdictReason string   `json:"verdict_reason,omitempty"`
	Excluded      []string `json:"excluded"`
	// FailureReason is absent unless the run failed for a reason the
	// control plane names.
	FailureReason string `json:"failure_reason,omitempty"`
	// BaseSHA is the base commit the contract was read at, ContractSHA the
	// blob of .forja.yaml there, HeadSHA the pull request's head.
	BaseSHA     string          `json:"base_sha,omitempty"`
	ContractSHA string          `json:"contract_sha,omitempty"`
	HeadSHA     string          `json:"head_sha,omitempty"`
	PullRequest *PullRequestRef `json:"pull_request,omitempty"`
	// Evidence is the report of the run, rendered once the diff was
	// classified; absent before.
	Evidence  string `json:"evidence,omitempty"`
	TokensIn  int64  `json:"tokens_in"`
	TokensOut int64  `json:"tokens_out"`
}

// readClass marks a tool as read in FuseOne's Gate.
func readClass() *sdk.ToolAnnotations {
	return &sdk.ToolAnnotations{ReadOnlyHint: true}
}

// writeClass marks a tool as write in FuseOne's Gate.
func writeClass() *sdk.ToolAnnotations {
	destructive := true
	return &sdk.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive}
}

func implementTool() *sdk.Tool {
	return &sdk.Tool{
		Name:        string(domain.ToolImplement),
		Description: "Implement an issue: branch, commits, gates and a PR with forja/gates.",
		Annotations: writeClass(),
	}
}

func verifyTool() *sdk.Tool {
	return &sdk.Tool{
		Name:        string(domain.ToolVerify),
		Description: "Verify a pull request a run opened against the base commit's contract; publishes forja/qa on its head.",
		Annotations: writeClass(),
	}
}
