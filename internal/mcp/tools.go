package mcp

import (
	"context"
	"errors"
	"fmt"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// ErrNotImplemented is returned by every tool whose behaviour has not landed
// yet. It travels as a tool error, never as a protocol error.
var ErrNotImplemented = errors.New("not implemented")

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
	Evidence  string `json:"evidence,omitempty"`
	TokensIn  int64  `json:"tokens_in"`
	TokensOut int64  `json:"tokens_out"`
	// ComputeMinutes and EstimatedCostUSDMicros are as forja.status reports
	// them.
	ComputeMinutes         float64      `json:"compute_minutes"`
	EstimatedCostUSDMicros *int64       `json:"estimated_cost_usd_micros,omitempty"`
	Setup                  []StepReport `json:"setup"`
	Gates                  []StepReport `json:"gates"`
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

// VerifyOutput is the result of forja.verify.
type VerifyOutput struct {
	RunID string `json:"run_id"`
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
		Description: "Verify a pull request: QA agent, inversions and gates; publishes forja/qa.",
		Annotations: writeClass(),
	}
}

// notImplemented is the handler of a tool whose slice has not landed. In and
// Out are constrained by any because sdk.AddTool fixes them per call site;
// the concrete types are the typed inputs and outputs declared above.
func notImplemented[In, Out any](name domain.Tool) sdk.ToolHandlerFor[In, Out] {
	return func(context.Context, *sdk.CallToolRequest, In) (*sdk.CallToolResult, Out, error) {
		var zero Out
		return nil, zero, fmt.Errorf("%s: %w", name, ErrNotImplemented)
	}
}
