package mcp

import (
	"context"
	"errors"
	"fmt"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/contract"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/github"
)

// Cloner is what forja.plan needs to reach the base branch: a checkout
// pinned at its tip, owned by the control plane.
type Cloner interface {
	Clone(ctx context.Context, repo, branch string) (Checkout, error)
}

// Checkout is a clone pinned at one commit. Every read answers for that
// commit; Dir is the tree on disk the agent and the gates run on; Close
// discards the clone. StartBranch checks a new branch out at the commit;
// Adopt imports that branch from a bundle the agent's workspace exported
// and checks its head out in Dir, while every read still answers for the
// pinned commit. DiffLines counts the lines an adopted head adds and
// deletes against the pinned commit.
type Checkout interface {
	SHA() string
	Branch() string
	Dir() string
	ReadFile(ctx context.Context, path string) ([]byte, error)
	ListFiles(ctx context.Context, dir string) ([]string, error)
	BlobSHA(ctx context.Context, path string) (string, error)
	StartBranch(ctx context.Context, name string) error
	Adopt(ctx context.Context, bundle, branch string) (string, error)
	DiffLines(ctx context.Context, head string) (int, error)
	Close() error
}

// IssueReader is what forja.plan needs from GitHub about the issue.
type IssueReader interface {
	Read(ctx context.Context, repo string, number int) (domain.Issue, error)
}

// PlanInput is the argument of forja.plan.
type PlanInput struct {
	Repository string `json:"repository" jsonschema:"repository in owner/name form"`
	Issue      int    `json:"issue" jsonschema:"issue number to plan"`
	BaseBranch string `json:"base_branch,omitempty" jsonschema:"branch the run starts from; empty means the default branch"`
}

// ContextFile is one agent_context file as the agent will read it.
type ContextFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// PlanOutput is the result of forja.plan: what the agent will see, read
// from the base branch at BaseSHA. AgentContext and Warnings are always on
// the wire, empty when there is nothing to say.
type PlanOutput struct {
	Repository string `json:"repository"`
	Issue      int    `json:"issue"`
	// BaseBranch is the branch checked out, resolved when the input left
	// it empty.
	BaseBranch string `json:"base_branch"`
	BaseSHA    string `json:"base_sha"`
	// ContractSHA is the blob SHA of .forja.yaml at BaseSHA: it names the
	// contract's bytes regardless of the commit they sit in, so later
	// evidence can say which contract applied without repeating BaseSHA.
	ContractSHA  string        `json:"contract_sha"`
	Title        string        `json:"title"`
	Body         string        `json:"body"`
	AgentContext []ContextFile `json:"agent_context"`
	Warnings     []string      `json:"warnings"`
}

func planTool() *sdk.Tool {
	return &sdk.Tool{
		Name:        string(domain.ToolPlan),
		Description: "Checkout and agent_context for an issue: what the agent will see.",
		Annotations: readClass(),
	}
}

// planHandler clones the base branch, reads the contract and the issue
// and assembles the agent's view. Every failure is a tool error; only a
// missing or binary agent_context path is a warning.
func planHandler(cloner Cloner, issues IssueReader) sdk.ToolHandlerFor[PlanInput, PlanOutput] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, in PlanInput) (*sdk.CallToolResult, PlanOutput, error) {
		out, err := plan(ctx, cloner, issues, in)
		if err != nil {
			return nil, PlanOutput{}, fmt.Errorf("%s: %w", domain.ToolPlan, err)
		}
		return nil, out, nil
	}
}

func plan(ctx context.Context, cloner Cloner, issues IssueReader, in PlanInput) (PlanOutput, error) {
	co, err := cloner.Clone(ctx, in.Repository, in.BaseBranch)
	if err != nil {
		return PlanOutput{}, err
	}
	// The result is what the agent sees; a clone directory that could not
	// be removed changes none of it, so that failure is not the tool's.
	defer func() { _ = co.Close() }()
	parsed, contractSHA, err := readContract(ctx, co, in.Repository)
	if err != nil {
		return PlanOutput{}, err
	}
	issue, err := issues.Read(ctx, in.Repository, in.Issue)
	if err != nil {
		return PlanOutput{}, err
	}
	files, warnings, err := agentContext(ctx, co, parsed.AgentContext)
	if err != nil {
		return PlanOutput{}, err
	}
	return PlanOutput{
		Repository:   in.Repository,
		Issue:        in.Issue,
		BaseBranch:   co.Branch(),
		BaseSHA:      co.SHA(),
		ContractSHA:  contractSHA,
		Title:        issue.Title,
		Body:         issue.Body,
		AgentContext: files,
		Warnings:     warnings,
	}, nil
}

// readContract parses .forja.yaml at the checkout's commit and returns it
// with its blob SHA. A missing file wraps ErrContractMissing; a rejected
// contract lists its defects and still names the blob they sit in.
func readContract(ctx context.Context, co Checkout, repo string) (domain.Contract, string, error) {
	body, err := co.ReadFile(ctx, ".forja.yaml")
	if errors.Is(err, github.ErrPathMissing) {
		return domain.Contract{}, "", fmt.Errorf("%s: %w at %s", repo, github.ErrContractMissing, co.SHA())
	}
	if err != nil {
		return domain.Contract{}, "", fmt.Errorf("read contract at %s: %w", co.SHA(), err)
	}
	sha, err := co.BlobSHA(ctx, ".forja.yaml")
	if err != nil {
		return domain.Contract{}, "", fmt.Errorf("read contract at %s: %w", co.SHA(), err)
	}
	parsed, err := contract.Parse(body)
	if err != nil {
		return domain.Contract{}, sha, fmt.Errorf("contract at %s rejected: %w", co.SHA(), err)
	}
	return parsed, sha, nil
}
