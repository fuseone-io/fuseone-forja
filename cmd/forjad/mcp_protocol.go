package main

import (
	"net/http"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/mcp"
)

// mcpProtocolVersion is the one protocol version the HTTP server speaks:
// the one where the closing of a request's stream cancels that request.
const mcpProtocolVersion = "2026-07-28"

// mcpHTTPServer is the server the HTTP transport serves: the tools, on
// mcpProtocolVersion alone.
func mcpHTTPServer(deps mcp.Deps) *sdk.Server {
	return mcp.New(deps, &sdk.ServerOptions{SupportedProtocolVersions: []string{mcpProtocolVersion}})
}

// requireProtocol answers 400 to a POST whose MCP-Protocol-Version is not
// mcpProtocolVersion, a missing one included: the SDK serves a request
// without the header on an older protocol, where a caller that goes away
// does not stop its call.
func requireProtocol(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.Header.Get("MCP-Protocol-Version") != mcpProtocolVersion {
			http.Error(w, "Bad Request: MCP-Protocol-Version must be "+mcpProtocolVersion, http.StatusBadRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}
