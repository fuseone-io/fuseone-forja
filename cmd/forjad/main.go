// Command forjad is Forja's binary: the MCP server over stdio, and the
// egress gateway and registry proxy the control plane starts next to each
// run's workspace.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/mcp"
)

const version = "0.0.0-dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	switch {
	case len(args) == 1 && args[0] == "serve":
		return serve(ctx, &sdk.StdioTransport{}, stderr, os.Getenv)
	case len(args) >= 1 && args[0] == "gateway":
		return gateway(ctx, args[1:], stdout, stderr)
	case len(args) >= 1 && args[0] == "registry":
		return registryProxy(ctx, args[1:], stdout, stderr)
	}
	usage(stderr)
	return 2
}

// serve recovers the claims a previous process left, then runs the server
// until the transport closes or ctx is cancelled. A cancelled context is
// the operator stopping the server, not a failure; a recovery that cannot
// finish is one.
func serve(ctx context.Context, transport sdk.Transport, stderr io.Writer, getenv func(string) string) int {
	deps, store, err := wire(getenv, stderr)
	if err != nil {
		// The exit code is the only channel left when stderr itself fails.
		_, _ = fmt.Fprintf(stderr, "forjad: serve: %v\n", err)
		return 1
	}
	if err := recoverClaims(ctx, store, stderr); err != nil {
		// The exit code is the only channel left when stderr itself fails.
		_, _ = fmt.Fprintf(stderr, "forjad: serve: %v\n", err)
		return 1
	}
	err = mcp.New(deps).Run(ctx, transport)
	if err == nil || errors.Is(err, context.Canceled) {
		return 0
	}
	// The exit code is the only channel left when stderr itself fails.
	_, _ = fmt.Fprintf(stderr, "forjad: serve: %v\n", err)
	return 1
}

func usage(w io.Writer) {
	// The exit code is the only channel left when stderr itself fails.
	_, _ = fmt.Fprint(w, "usage: forjad serve\n       forjad gateway -run <id> [-allow host:port,...] [-listen addr]\n"+
		"       forjad registry -run <id> -registries entry,... -credentials file [-listen addr]\n\n"+
		"  serve     run the MCP server over stdio\n"+
		"  gateway   run the egress gateway of one run's workspace\n"+
		"  registry  run the registry proxy of one run's workspace\n")
}
