package mcp

import (
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// ImplementInput is the argument of forja.implement. Branch and Finding
// together make a correction round on the pull request Forja opened from
// forja/<issue>; both empty is a first run.
type ImplementInput struct {
	Repository string `json:"repository" jsonschema:"repository in owner/name form"`
	Issue      int    `json:"issue" jsonschema:"issue number to implement"`
	BaseBranch string `json:"base_branch" jsonschema:"branch the PR targets"`
	Branch     string `json:"branch,omitempty" jsonschema:"for a correction round: forja/<issue>, the branch of the pull request Forja opened; set together with finding"`
	Finding    string `json:"finding,omitempty" jsonschema:"for a correction round: the review finding to address, at most 64 KiB; set together with branch"`
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
	Evidence string `json:"evidence,omitempty"`
	// WallClockSeconds and MaxDiffLines are the limits the run worked
	// under, absent before it read its contract; DiffLines is what its head
	// adds and deletes against the base, absent until counted.
	WallClockSeconds float64 `json:"wall_clock_seconds,omitempty"`
	MaxDiffLines     int     `json:"max_diff_lines,omitempty"`
	DiffLines        *int    `json:"diff_lines,omitempty"`
	TokensIn         int64   `json:"tokens_in"`
	TokensOut        int64   `json:"tokens_out"`
	// ComputeMinutes and EstimatedCostUSDMicros are as forja.status reports
	// them.
	ComputeMinutes         float64      `json:"compute_minutes"`
	EstimatedCostUSDMicros *int64       `json:"estimated_cost_usd_micros,omitempty"`
	Setup                  []StepReport `json:"setup"`
	Gates                  []StepReport `json:"gates"`
	// Attempt is the last attempt's number and MaxAttempts the base
	// contract's limit, both 0 when no agent ran; Attempts lists every
	// attempt whose gates ran, always on the wire.
	Attempt     int             `json:"attempt"`
	MaxAttempts int             `json:"max_attempts"`
	Attempts    []AttemptReport `json:"attempts"`
	// HumanMerge says the agent's head changed an existing file of the base
	// contract's proof_paths, listed in ProofApparatus, or a path under its
	// human_merge_paths, listed in HumanMergePaths. All three are always on
	// the wire, the lists empty when nothing matched.
	HumanMerge      bool          `json:"human_merge"`
	ProofApparatus  []ChangedPath `json:"proof_apparatus"`
	HumanMergePaths []ChangedPath `json:"human_merge_paths"`
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
// verdict was reached on, and the verdict. A verdict that does not bite,
// and a run no inversion could decide, are results, not tool errors.
// Excluded and Inversions are always on the wire, empty when no changed
// path matched verify.exclude or no candidate was looked at.
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
	// ComputeMinutes and EstimatedCostUSDMicros are this verify run's own,
	// its QA agent's and its gates', as forja.status reports them.
	ComputeMinutes         float64           `json:"compute_minutes"`
	EstimatedCostUSDMicros *int64            `json:"estimated_cost_usd_micros,omitempty"`
	Inversions             []InversionReport `json:"inversions"`
}

// InversionReport is one QA candidate as forja.verify answers it. Paths is
// always on the wire; Commit is absent for a candidate with no commit,
// Rejected for an admitted one, Outcome for one whose gates showed
// neither, and FailedStep when every step passed.
type InversionReport struct {
	Branch     string   `json:"branch"`
	Commit     string   `json:"commit,omitempty"`
	Paths      []string `json:"paths"`
	Admitted   bool     `json:"admitted"`
	Rejected   string   `json:"rejected,omitempty"`
	Outcome    string   `json:"outcome,omitempty"`
	FailedStep string   `json:"failed_step,omitempty"`
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
