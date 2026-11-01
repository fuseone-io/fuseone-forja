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

func emptyEnv(string) string { return "" }

func TestServe_contextCancelled_exitsZeroSilently(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, serverTransport := sdk.NewInMemoryTransports()

	var stderr bytes.Buffer
	if code := serve(ctx, serverTransport, &stderr, emptyEnv); code != 0 {
		t.Fatalf("serve after cancellation = %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("a stop by signal is not a failure, got stderr %q", stderr.String())
	}
}

// serveWithoutCredentials starts serve with an empty environment and
// connects a client. finish stops the server and asserts it exited clean:
// missing credentials are not a startup failure.
func serveWithoutCredentials(t *testing.T) (*sdk.ClientSession, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	clientTransport, serverTransport := sdk.NewInMemoryTransports()
	var stderr bytes.Buffer
	exited := make(chan int, 1)
	go func() { exited <- serve(ctx, serverTransport, &stderr, emptyEnv) }()

	client := sdk.NewClient(&sdk.Implementation{Name: "forjad-test-client", Version: "test"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		cancel()
		t.Fatalf("client.Connect: %v", err)
	}
	finish := func() {
		_ = cs.Close()
		cancel()
		if code := <-exited; code != 0 {
			t.Fatalf("serve = %d, want 0", code)
		}
		if stderr.Len() != 0 {
			t.Fatalf("missing credentials are not a startup failure, got stderr %q", stderr.String())
		}
	}
	return cs, finish
}

// assertNamesCredentialVariables checks a tool error that says which
// variables to set instead of failing later with a 401.
func assertNamesCredentialVariables(t *testing.T, tool string, res *sdk.CallToolResult) {
	t.Helper()
	if !res.IsError {
		t.Fatalf("%s succeeded with no credentials: %v", tool, res.StructuredContent)
	}
	text := ""
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			text += tc.Text
		}
	}
	for _, want := range []string{tool, "FORJA_GITHUB_APP_ID", "FORJA_GITHUB_TOKEN"} {
		if !strings.Contains(text, want) {
			t.Errorf("error text %q does not name %s", text, want)
		}
	}
}

// Without credentials the server still serves; only forja.doctor fails.
func TestServe_noCredentials_startsAndDoctorFailsExplicitly(t *testing.T) {
	t.Parallel()
	cs, finish := serveWithoutCredentials(t)

	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      "forja.doctor",
		Arguments: map[string]any{"repository": "fuseone-io/fuseone-forja"},
	})
	if err != nil {
		t.Fatalf("CallTool(forja.doctor): protocol error %v", err)
	}
	assertNamesCredentialVariables(t, "forja.doctor", res)
	finish()
}

// forja.plan needs the same credentials for its clone and its issue read;
// without them it fails before touching git or the API.
func TestServe_noCredentials_planFailsExplicitly(t *testing.T) {
	t.Parallel()
	cs, finish := serveWithoutCredentials(t)

	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      "forja.plan",
		Arguments: map[string]any{"repository": "fuseone-io/fuseone-forja", "issue": 7},
	})
	if err != nil {
		t.Fatalf("CallTool(forja.plan): protocol error %v", err)
	}
	assertNamesCredentialVariables(t, "forja.plan", res)
	finish()
}

// The context is already cancelled: a serve that got as far as running the
// server would return 0 at once instead of hanging.
func TestServe_misconfiguredApp_exitsOneNamingTheVariable(t *testing.T) {
	t.Parallel()
	env := map[string]string{"FORJA_GITHUB_APP_ID": "12345", "FORJA_GITHUB_INSTALLATION_ID": "777"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, serverTransport := sdk.NewInMemoryTransports()

	var stderr bytes.Buffer
	if code := serve(ctx, serverTransport, &stderr, func(k string) string { return env[k] }); code != 1 {
		t.Fatalf("serve with a half-configured App = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "FORJA_GITHUB_PRIVATE_KEY_FILE") {
		t.Fatalf("stderr %q does not name the missing variable", stderr.String())
	}
}
