package main

import (
	"errors"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/caller"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// errNoAuthenticatedClient is a call over HTTP that reached a tool with no
// client of the clients file on it.
var errNoAuthenticatedClient = errors.New("no authenticated client on the request")

// registryCallers names the client of a call over HTTP: the one the bearer
// check put on the request, scoped as the clients file says.
type registryCallers struct {
	reg caller.Registry
}

func (r registryCallers) Caller(req *sdk.CallToolRequest) (domain.Client, error) {
	extra := req.GetExtra()
	if extra == nil || extra.TokenInfo == nil {
		return domain.Client{}, errNoAuthenticatedClient
	}
	c, ok := r.reg.Lookup(extra.TokenInfo.UserID)
	if !ok {
		return domain.Client{}, errNoAuthenticatedClient
	}
	return c, nil
}

// stdioCallers names the client of a call over stdio: the process that
// started the server, which reaches every repository the App does.
type stdioCallers struct{}

func (stdioCallers) Caller(*sdk.CallToolRequest) (domain.Client, error) {
	return domain.Client{Name: domain.StdioClientName, Unrestricted: true}, nil
}
