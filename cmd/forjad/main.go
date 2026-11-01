// Command forjad is Forja's binary. Today it serves the MCP server over stdio.
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
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

const version = "0.0.0-dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "serve" {
		usage(stderr)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return serve(ctx, &sdk.StdioTransport{}, stderr)
}

// serve runs the server until the transport closes or ctx is cancelled. A
// cancelled context is the operator stopping the server, not a failure.
func serve(ctx context.Context, transport sdk.Transport, stderr io.Writer) int {
	server := mcp.New(runstore.NewMemory(), version)
	err := server.Run(ctx, transport)
	if err == nil || errors.Is(err, context.Canceled) {
		return 0
	}
	// The exit code is the only channel left when stderr itself fails.
	_, _ = fmt.Fprintf(stderr, "forjad: serve: %v\n", err)
	return 1
}

func usage(w io.Writer) {
	// The exit code is the only channel left when stderr itself fails.
	_, _ = fmt.Fprint(w, "usage: forjad serve\n\n  serve  run the MCP server over stdio\n")
}
