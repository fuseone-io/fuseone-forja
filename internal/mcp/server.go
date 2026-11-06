// Package mcp is Forja's MCP server: the five tools, their schemas and their
// read/write class as FuseOne's Gate reads it.
package mcp

import (
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// Deps is everything the tools need from the edge: the stores, the
// repository readers, the gates and agent runners, and the clock and id generator
// injected so that the handlers stay deterministic.
type Deps struct {
	// Callers names the client of every tool call; with none, every call
	// is refused.
	Callers   Callers
	Runs      RunStore
	Contracts ContractReader
	Claims    Claimer
	Cloner    Cloner
	Issues    IssueReader
	Gates     GateRunner
	// Agent runs the agent of a forja.implement run; nil until the model
	// proxy is configured.
	Agent AgentRunner
	// Pusher, PullRequests and Statuses deliver a green implement run: its
	// branch, its pull request and its forja/gates status.
	Pusher       Pusher
	PullRequests PullRequests
	Statuses     CommitStatuses
	// BranchCloner and Identity serve a correction round: the run's branch
	// at its tip, and the login a pull request Forja opened is authored by.
	BranchCloner BranchCloner
	Identity     Identity
	// PullRequestReader reads the pull request a forja.verify run verifies.
	PullRequestReader PullRequestReader
	// QA runs the QA agent of a forja.verify run; nil until the model proxy
	// is configured.
	QA QAAgent
	// NewID yields the id of a new run.
	NewID func() string
	Now   func() time.Time
	// Lease overrides the lease a run holds its issue for; zero means the
	// contract's wall clock plus a margin.
	Lease time.Duration
	// Prices is the operator's price table the run's cost is estimated
	// with when it is answered; nil means no estimate.
	Prices  *domain.PriceTable
	Version string
}

// New builds the server with the five tools registered, each backed by
// its handler and served only to a client deps.Callers names, for the
// repositories that client reaches. opts is the transport's say over the
// server, such as the protocol versions it speaks; nil is the SDK's defaults.
func New(deps Deps, opts *sdk.ServerOptions) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "forja", Version: deps.Version}, opts)
	c := deps.Callers
	sdk.AddTool(server, doctorTool(), scoped(domain.ToolDoctor, c, named(func(in DoctorInput) string { return in.Repository }), doctorHandler(deps)))
	sdk.AddTool(server, planTool(), scoped(domain.ToolPlan, c, named(func(in PlanInput) string { return in.Repository }), planHandler(deps.Cloner, deps.Issues)))
	sdk.AddTool(server, implementTool(), scoped(domain.ToolImplement, c, named(func(in ImplementInput) string { return in.Repository }), implementHandler(deps)))
	sdk.AddTool(server, verifyTool(), scoped(domain.ToolVerify, c, named(func(in VerifyInput) string { return in.Repository }), verifyHandler(deps)))
	sdk.AddTool(server, statusTool(), scoped(domain.ToolStatus, c, statusRepository, statusHandler(deps.Runs, deps.Prices)))
	return server
}

// statusRepository is the repository a list is filtered on; a call by id
// or an unfiltered list names none.
func statusRepository(in StatusInput) (string, bool) {
	return in.Repository, in.Repository != ""
}
