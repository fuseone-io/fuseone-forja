// Command forjad is Forja's binary: the MCP server over stdio, and the
// egress gateway, registry proxy and model proxy the control plane starts
// next to each run's workspace.
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
	case len(args) == 1 && args[0] == "migrate":
		return migrate(ctx, stdout, stderr, os.Getenv)
	case len(args) >= 1 && args[0] == "gateway":
		return gateway(ctx, args[1:], stdout, stderr)
	case len(args) >= 1 && args[0] == "registry":
		return registryProxy(ctx, args[1:], stdout, stderr)
	case len(args) >= 1 && args[0] == "model":
		return model(ctx, args[1:], stdout, stderr)
	}
	usage(stderr)
	return 2
}

// serve is the MCP server with the process's environment and network.
func serve(ctx context.Context, transport sdk.Transport, stderr io.Writer, getenv func(string) string) int {
	return host{getenv: getenv, listen: listenTCP}.serve(ctx, transport, stderr)
}

// serve opens the run store, recovers the claims a previous process left
// and closes their runs, opens the metrics listener when one is
// configured, then runs the server until the transport closes or ctx is
// cancelled, and closes the listener and the store with it. A
// cancelled context is the operator stopping the server, not a failure; a
// recovery that cannot finish or a listener that cannot open is one.
func (h host) serve(ctx context.Context, transport sdk.Transport, stderr io.Writer) int {
	addr, err := metricsAddr(h.getenv)
	if err != nil {
		return fail(stderr, err)
	}
	deps, store, err := wire(h.getenv, stderr)
	if err != nil {
		return fail(stderr, err)
	}
	runs, closeRuns, err := openRuns(ctx, h.getenv)
	if err != nil {
		return fail(stderr, err)
	}
	defer closeRuns()
	deps.Runs = runs
	if err := recoverClaims(ctx, store, deps, stderr); err != nil {
		return fail(stderr, err)
	}
	stop, err := h.startMetrics(ctx, stderr, addr, deps)
	if err != nil {
		return fail(stderr, err)
	}
	err = mcp.New(deps).Run(ctx, transport)
	stop()
	if err == nil || errors.Is(err, context.Canceled) {
		return 0
	}
	return fail(stderr, err)
}

func fail(stderr io.Writer, err error) int {
	// The exit code is the only channel left when stderr itself fails.
	_, _ = fmt.Fprintf(stderr, "forjad: serve: %v\n", err)
	return 1
}

func usage(w io.Writer) {
	// The exit code is the only channel left when stderr itself fails.
	_, _ = fmt.Fprint(w, "usage: forjad serve\n       forjad migrate\n       forjad gateway -run <id> [-allow host:port,...] [-listen addr]\n"+
		"       forjad registry -run <id> -registries entry,... -credentials file [-listen addr]\n"+
		"       forjad model -run <id> -upstream <url> -cap <n> -key-file <path> -token-file <path> [-listen addr]\n\n"+
		"  serve     run the MCP server over stdio\n"+
		"  migrate   apply the run store's schema to FORJA_DATABASE_URL\n"+
		"  gateway   run the egress gateway of one run's workspace\n"+
		"  registry  run the registry proxy of one run's workspace\n"+
		"  model     run the model proxy of one run's agent\n")
}
