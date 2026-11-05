package mcp

import (
	"context"
	"errors"
	"fmt"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// Callers names the client a tool call comes from, from what the transport
// put on its request. A call it cannot name is refused.
type Callers interface {
	Caller(req *sdk.CallToolRequest) (domain.Client, error)
}

// ErrRepositoryNotReached is a tool call naming a repository outside the
// calling client's repositories.
var ErrRepositoryNotReached = errors.New("not among the client's repositories")

// errNoClient is a call no client could be named for.
var errNoClient = errors.New("no client named for the call")

type clientKey struct{}

// withClient is ctx carrying the client of the call it serves.
func withClient(ctx context.Context, c domain.Client) context.Context {
	return context.WithValue(ctx, clientKey{}, c)
}

// clientOf is the client ctx carries; the zero Client, which owns no run
// and reaches no repository, when it carries none.
func clientOf(ctx context.Context) domain.Client {
	c, _ := ctx.Value(clientKey{}).(domain.Client)
	return c
}

// repositoryOf is the repository a tool's input names, and whether it
// names one; only forja.status may leave it out. In is any tool input
// struct, as sdk.ToolHandlerFor allows.
type repositoryOf[In any] func(In) (string, bool)

// scoped serves h only to a call whose client callers can name and which
// names no repository outside that client's, before h reads anything; h
// finds the client on its context. A refusal is a tool error naming tool.
// The type parameters are the handler's own input and output.
func scoped[In, Out any](tool domain.Tool, callers Callers, repo repositoryOf[In], h sdk.ToolHandlerFor[In, Out]) sdk.ToolHandlerFor[In, Out] {
	return func(ctx context.Context, req *sdk.CallToolRequest, in In) (*sdk.CallToolResult, Out, error) {
		c, err := admit(callers, req, repo, in)
		if err != nil {
			var zero Out
			return nil, zero, fmt.Errorf("%s: %w", tool, err)
		}
		return h(withClient(ctx, c), req, in)
	}
}

// admit names the call's client and checks the repository its input names
// against it; In is the tool's input, any struct the SDK decodes.
func admit[In any](callers Callers, req *sdk.CallToolRequest, repo repositoryOf[In], in In) (domain.Client, error) {
	if callers == nil {
		return domain.Client{}, errors.New("not wired: callers required")
	}
	c, err := callers.Caller(req)
	if err != nil {
		return domain.Client{}, fmt.Errorf("%w: %w", errNoClient, err)
	}
	if c.Name == "" {
		return domain.Client{}, errNoClient
	}
	if r, names := repo(in); names && !c.Reaches(r) {
		return domain.Client{}, fmt.Errorf("repository %q: %w", r, ErrRepositoryNotReached)
	}
	return c, nil
}

// named is the repository of a tool that always names one; In is the
// tool's input, any struct the SDK decodes.
func named[In any](field func(In) string) repositoryOf[In] {
	return func(in In) (string, bool) { return field(in), true }
}
