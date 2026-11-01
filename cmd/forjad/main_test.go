package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRun_notServe_usageAndExit2(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "no subcommand", args: nil},
		{name: "unknown subcommand", args: []string{"doctor"}},
		{name: "serve with extra argument", args: []string{"serve", "extra"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stderr bytes.Buffer
			if code := run(tt.args, &stderr); code != 2 {
				t.Fatalf("run(%q) = %d, want 2", tt.args, code)
			}
			if !strings.Contains(stderr.String(), "usage: forjad serve") {
				t.Fatalf("stderr %q does not print the usage", stderr.String())
			}
		})
	}
}

func TestServe_contextCancelled_exitsZeroSilently(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, serverTransport := sdk.NewInMemoryTransports()

	var stderr bytes.Buffer
	if code := serve(ctx, serverTransport, &stderr); code != 0 {
		t.Fatalf("serve after cancellation = %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("a stop by signal is not a failure, got stderr %q", stderr.String())
	}
}
