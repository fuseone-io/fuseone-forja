// Package mcp is Forja's MCP server: the five tools, their schemas and their
// read/write class as FuseOne's Gate reads it.
package mcp

import (
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// Deps is everything the tools need from the edge: the stores, the
// repository readers, and the clock and id generator injected so that the
// handlers stay deterministic.
type Deps struct {
	Runs      RunStore
	Contracts ContractReader
	Claims    Claimer
	// NewID yields the id of a new run.
	NewID func() string
	Now   func() time.Time
	// Lease overrides the lease a run holds its issue for; zero means the
	// contract's wall clock plus a margin.
	Lease   time.Duration
	Version string
}

// New builds the server with every tool registered. forja.status,
// forja.doctor and forja.implement are backed by behaviour; the others
// answer ErrNotImplemented.
func New(deps Deps) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "forja", Version: deps.Version}, nil)
	sdk.AddTool(server, doctorTool(), doctorHandler(deps.Contracts))
	sdk.AddTool(server, planTool(), notImplemented[PlanInput, PlanOutput](domain.ToolPlan))
	sdk.AddTool(server, implementTool(), implementHandler(deps))
	sdk.AddTool(server, verifyTool(), notImplemented[VerifyInput, VerifyOutput](domain.ToolVerify))
	sdk.AddTool(server, statusTool(), statusHandler(deps.Runs))
	return server
}
