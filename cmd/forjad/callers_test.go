package main

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/mcp"
)

func callWith(extra *sdk.RequestExtra) *sdk.CallToolRequest {
	return &sdk.CallToolRequest{Extra: extra}
}

// Over HTTP the client is the one the bearer authenticated, with its own
// repositories; a request that carries no authenticated client is refused.
func TestRegistryCallers_clientOfTheTokenInfoOrError(t *testing.T) {
	t.Parallel()
	callers := registryCallers{reg: testRegistry(t)}
	got, err := callers.Caller(callWith(&sdk.RequestExtra{TokenInfo: &auth.TokenInfo{UserID: "fuseone-pilot"}}))
	if err != nil || got.Name != "fuseone-pilot" || got.Unrestricted || !got.Reaches("acme/payments") || got.Reaches("acme/ledger") {
		t.Fatalf("Caller = (%+v, %v), want fuseone-pilot restricted to acme/payments", got, err)
	}
	for name, req := range map[string]*sdk.CallToolRequest{
		"no extra":                 callWith(nil),
		"no token info":            callWith(&sdk.RequestExtra{}),
		"no user":                  callWith(&sdk.RequestExtra{TokenInfo: &auth.TokenInfo{}}),
		"unknown user":             callWith(&sdk.RequestExtra{TokenInfo: &auth.TokenInfo{UserID: "someone-else"}}),
		"the stdio client's name":  callWith(&sdk.RequestExtra{TokenInfo: &auth.TokenInfo{UserID: domain.StdioClientName}}),
		"the name in another case": callWith(&sdk.RequestExtra{TokenInfo: &auth.TokenInfo{UserID: "FUSEONE-PILOT"}}),
	} {
		if c, err := callers.Caller(req); err == nil || c.Name != "" {
			t.Fatalf("%s: Caller = (%+v, %v), want an error and no client", name, c, err)
		}
	}
}

// Over stdio the process that started Forja is the client: one fixed
// name, every repository, whatever the request carries.
func TestStdioCallers_fixedClientEveryRepository(t *testing.T) {
	t.Parallel()
	for _, req := range []*sdk.CallToolRequest{callWith(nil), callWith(&sdk.RequestExtra{TokenInfo: &auth.TokenInfo{UserID: "fuseone-pilot"}})} {
		got, err := (stdioCallers{}).Caller(req)
		if err != nil || got.Name != domain.StdioClientName || !got.Reaches("anyone/anything") {
			t.Fatalf("Caller = (%+v, %v), want %s reaching every repository", got, err, domain.StdioClientName)
		}
	}
}

func toolText(res *sdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// Over HTTP a tool naming a repository outside the bearer's client is
// refused as a tool error, and one inside goes on to the credentials;
// over stdio no repository is outside.
func TestServe_repositoryScope_perTransport(t *testing.T) {
	t.Parallel()
	endpoint, cancel, exited, _ := servedHTTP(t, map[string]string{}, "http")
	cs, err := connectHTTP(t, endpoint, testBearer, http.DefaultTransport)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	doctor := func(cs *sdk.ClientSession, repo string) string {
		res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "forja.doctor", Arguments: map[string]any{"repository": repo}})
		if err != nil || !res.IsError {
			t.Fatalf("forja.doctor %s = (%v, %v), want a tool error", repo, res, err)
		}
		return toolText(res)
	}
	if text := doctor(cs, "evil/payments"); !strings.Contains(text, mcp.ErrRepositoryNotReached.Error()) {
		t.Fatalf("HTTP, outside the client: %q, want the scope's refusal", text)
	}
	if text := doctor(cs, "Acme/Payments"); strings.Contains(text, mcp.ErrRepositoryNotReached.Error()) || !strings.Contains(text, "FORJA_GITHUB_TOKEN") {
		t.Fatalf("HTTP, inside the client: %q, want past the scope to the credentials", text)
	}
	_ = cs.Close()
	cancel()
	<-exited

	stdio, finish := serveWithoutCredentials(t)
	if text := doctor(stdio, "evil/payments"); strings.Contains(text, mcp.ErrRepositoryNotReached.Error()) {
		t.Fatalf("stdio: %q, want every repository reached", text)
	}
	finish()
}
