// Package mcp is Forja's MCP server: the five tools, their schemas and their
// read/write class as FuseOne's Gate reads it.
package mcp

import (
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// Deps is everything the tools reach outside the process.
type Deps struct {
	Runs      RunReader
	Contracts ContractReader
	Cloner    Cloner
	Issues    IssueReader
}

// New builds the server with every tool registered. forja.status,
// forja.doctor and forja.plan are backed by behaviour; the others answer
// ErrNotImplemented.
func New(deps Deps, version string) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "forja", Version: version}, nil)
	sdk.AddTool(server, doctorTool(), doctorHandler(deps.Contracts))
	sdk.AddTool(server, planTool(), planHandler(deps.Cloner, deps.Issues))
	sdk.AddTool(server, implementTool(), notImplemented[ImplementInput, ImplementOutput](domain.ToolImplement))
	sdk.AddTool(server, verifyTool(), notImplemented[VerifyInput, VerifyOutput](domain.ToolVerify))
	sdk.AddTool(server, statusTool(), statusHandler(deps.Runs))
	return server
}
