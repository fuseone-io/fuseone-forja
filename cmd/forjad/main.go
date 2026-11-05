// Command forjad is Forja's binary: the MCP server over stdio or HTTP, and the
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
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch {
	case cmd == "serve" && len(rest) == 0:
		return serve(ctx, &sdk.StdioTransport{}, stderr, os.Getenv)
	case cmd == "migrate" && len(rest) == 0:
		return migrate(ctx, stdout, stderr, os.Getenv)
	case cmd == "gateway":
		return gateway(ctx, rest, stdout, stderr)
	case cmd == "registry":
		return registryProxy(ctx, rest, stdout, stderr)
	case cmd == "model":
		return model(ctx, rest, stdout, stderr)
	}
	usage(stderr)
	return 2
}

// serve is the MCP server with the process's environment and network.
func serve(ctx context.Context, transport sdk.Transport, stderr io.Writer, getenv func(string) string) int {
	return host{getenv: getenv, listen: listenTCP}.serve(ctx, transport, stderr)
}

// serve locks the state directory, opens the run store, recovers the
// claims a previous process left and closes their runs, opens the metrics
// listener when one is configured, then runs the server until the
// transport closes or ctx is cancelled, and closes the listener, the
// store and the lock with it. The lock comes before everything else: the
// journal it guards is read as a dead process's only while no live
// process holds the directory. A cancelled context is the operator
// stopping the server, not a failure; a directory another serve holds, a
// recovery that cannot finish or a listener that cannot open is one.
func (h host) serve(ctx context.Context, transport sdk.Transport, stderr io.Writer) int {
	release, err := holdStateDir(h.getenv)
	if err != nil {
		return fail(stderr, err)
	}
	defer release()
	addr, err := metricsAddr(h.getenv)
	if err != nil {
		return fail(stderr, err)
	}
	httpCfg, err := mcpHTTPConfig(h.getenv)
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
	stop, ready, err := h.startMetrics(ctx, stderr, addr, deps)
	if err != nil {
		return fail(stderr, err)
	}
	if httpCfg.addr != "" {
		err = h.serveMCPHTTP(ctx, mcp.New(deps), httpCfg, httpSinks{log: stderr, ready: ready})
	} else {
		err = serveMCP(ctx, mcp.New(deps), transport, ready)
	}
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
		"  serve     run the MCP server over stdio, or over HTTP when FORJA_MCP_ADDR is set\n"+
		"  migrate   apply the run store's schema to FORJA_DATABASE_URL\n"+
		"  gateway   run the egress gateway of one run's workspace\n"+
		"  registry  run the registry proxy of one run's workspace\n"+
		"  model     run the model proxy of one run's agent\n")
}
