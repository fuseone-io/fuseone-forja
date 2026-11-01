// Package mcp is Forja's MCP server: the five tools, their schemas and their
// read/write class as FuseOne's Gate reads it.
package mcp

import (
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// New builds the server with every tool registered. forja.status and
// forja.doctor are backed by behaviour; the others answer ErrNotImplemented.
func New(runs RunReader, contracts ContractReader, version string) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "forja", Version: version}, nil)
	sdk.AddTool(server, doctorTool(), doctorHandler(contracts))
	sdk.AddTool(server, planTool(), notImplemented[PlanInput, PlanOutput](domain.ToolPlan))
	sdk.AddTool(server, implementTool(), notImplemented[ImplementInput, ImplementOutput](domain.ToolImplement))
	sdk.AddTool(server, verifyTool(), notImplemented[VerifyInput, VerifyOutput](domain.ToolVerify))
	sdk.AddTool(server, statusTool(), statusHandler(runs))
	return server
}
