package main

import (
	"context"
	"net/http"
	"sync"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// headerProbe serves one tool that records the Authorization header and
// the client the request reached it with.
type headerProbe struct {
	mu            sync.Mutex
	authorization []string
	users         []string
}

func (p *headerProbe) server() *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "probe", Version: "test"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "forja.probe"}, func(_ context.Context, req *sdk.CallToolRequest, _ probeIn) (*sdk.CallToolResult, probeOut, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		extra := req.GetExtra()
		p.authorization = append(p.authorization, extra.Header.Values("Authorization")...)
		if extra.TokenInfo != nil {
			p.users = append(p.users, extra.TokenInfo.UserID)
		}
		return nil, probeOut{RunID: "r-1"}, nil
	})
	return server
}

// No tool can read the bearer: the request reaches the SDK with the
// client authenticated and without its Authorization header.
func TestMCPHandler_toolCall_noAuthorizationHeaderClientStillKnown(t *testing.T) {
	t.Parallel()
	probe := &headerProbe{}
	h := mcpHandler(probe.server(), testRegistry(t))
	if rec := post(t, h, "Bearer "+testBearer, callBody("x")); rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	if len(probe.authorization) != 0 {
		t.Fatalf("the tool read Authorization %q", probe.authorization)
	}
	if len(probe.users) != 1 || probe.users[0] != "fuseone-pilot" {
		t.Fatalf("the tool saw clients %q, want [fuseone-pilot]", probe.users)
	}
}
