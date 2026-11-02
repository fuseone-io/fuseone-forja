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

// New builds the server with every tool registered. forja.status,
// forja.doctor, forja.plan and forja.implement are backed by behaviour;
// forja.verify answers ErrNotImplemented.
func New(deps Deps) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "forja", Version: deps.Version}, nil)
	sdk.AddTool(server, doctorTool(), doctorHandler(deps))
	sdk.AddTool(server, planTool(), planHandler(deps.Cloner, deps.Issues))
	sdk.AddTool(server, implementTool(), implementHandler(deps))
	sdk.AddTool(server, verifyTool(), notImplemented[VerifyInput, VerifyOutput](domain.ToolVerify))
	sdk.AddTool(server, statusTool(), statusHandler(deps.Runs, deps.Prices))
	return server
}
