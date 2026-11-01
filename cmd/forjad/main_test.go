package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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

// Without credentials the server still serves; only forja.doctor fails,
// and it says which variables to set instead of failing later with a 401.
func TestServe_noCredentials_startsAndDoctorFailsExplicitly(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clientTransport, serverTransport := sdk.NewInMemoryTransports()
	var stderr bytes.Buffer
	exited := make(chan int, 1)
	go func() { exited <- serve(ctx, serverTransport, &stderr, emptyEnv) }()

	client := sdk.NewClient(&sdk.Implementation{Name: "forjad-test-client", Version: "test"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	res, err := cs.CallTool(ctx, &sdk.CallToolParams{
		Name:      "forja.doctor",
		Arguments: map[string]any{"repository": "fuseone-io/fuseone-forja"},
	})
	if err != nil {
		t.Fatalf("CallTool(forja.doctor): protocol error %v", err)
	}
	if !res.IsError {
		t.Fatalf("forja.doctor succeeded with no credentials: %v", res.StructuredContent)
	}
	text := ""
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			text += tc.Text
		}
	}
	for _, want := range []string{"FORJA_GITHUB_APP_ID", "FORJA_GITHUB_TOKEN"} {
		if !strings.Contains(text, want) {
			t.Errorf("error text %q does not name %s", text, want)
		}
	}
	_ = cs.Close()
	cancel()
	if code := <-exited; code != 0 {
		t.Fatalf("serve = %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("missing credentials are not a startup failure, got stderr %q", stderr.String())
	}
}

// With credentials but no state directory the server serves, and says
// once on stderr that a claim left by a crash is only freed by its lease.
func TestServe_noStateDir_warnsAndServes(t *testing.T) {
	t.Parallel()
	env := map[string]string{"FORJA_GITHUB_TOKEN": "github_pat_dev"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, serverTransport := sdk.NewInMemoryTransports()

	var stderr bytes.Buffer
	if code := serve(ctx, serverTransport, &stderr, func(k string) string { return env[k] }); code != 0 {
		t.Fatalf("serve without a state directory = %d, want 0: %s", code, stderr.String())
	}
	for _, want := range []string{"FORJA_STATE_DIR", "lease"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr %q does not mention %s", stderr.String(), want)
		}
	}
	if lines := strings.Count(stderr.String(), "\n"); lines != 1 {
		t.Errorf("stderr has %d lines, want exactly one warning: %q", lines, stderr.String())
	}
}

func TestServe_stateDirSetAndEmpty_servesSilently(t *testing.T) {
	t.Parallel()
	env := map[string]string{"FORJA_GITHUB_TOKEN": "github_pat_dev", "FORJA_STATE_DIR": t.TempDir()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, serverTransport := sdk.NewInMemoryTransports()

	var stderr bytes.Buffer
	if code := serve(ctx, serverTransport, &stderr, func(k string) string { return env[k] }); code != 0 {
		t.Fatalf("serve with an empty state directory = %d, want 0: %s", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("an empty journal is nothing to report, got stderr %q", stderr.String())
	}
}

// A journal that cannot be read means claims may be left behind that the
// server cannot account for: that is a startup failure, not a warning.
func TestServe_corruptJournal_exitsOne(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "claims"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "claims", "o__r__7.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"FORJA_GITHUB_TOKEN": "github_pat_dev", "FORJA_STATE_DIR": dir}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, serverTransport := sdk.NewInMemoryTransports()

	var stderr bytes.Buffer
	if code := serve(ctx, serverTransport, &stderr, func(k string) string { return env[k] }); code != 1 {
		t.Fatalf("serve with a corrupt journal = %d, want 1: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "o__r__7.json") {
		t.Fatalf("stderr %q does not name the corrupt entry", stderr.String())
	}
}

func TestServe_invalidClaimLease_exitsOneNamingTheVariable(t *testing.T) {
	t.Parallel()
	tests := map[string]string{"not a duration": "soon", "zero": "0s", "negative": "-5m"}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env := map[string]string{"FORJA_GITHUB_TOKEN": "github_pat_dev", "FORJA_CLAIM_LEASE": value}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, serverTransport := sdk.NewInMemoryTransports()

			var stderr bytes.Buffer
			if code := serve(ctx, serverTransport, &stderr, func(k string) string { return env[k] }); code != 1 {
				t.Fatalf("serve with FORJA_CLAIM_LEASE=%q = %d, want 1", value, code)
			}
			if !strings.Contains(stderr.String(), "FORJA_CLAIM_LEASE") {
				t.Fatalf("stderr %q does not name the variable", stderr.String())
			}
		})
	}
}

// Without credentials forja.implement fails before reserving anything,
// naming the variables to set.
func TestServe_noCredentials_implementFailsExplicitly(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clientTransport, serverTransport := sdk.NewInMemoryTransports()
	var stderr bytes.Buffer
	exited := make(chan int, 1)
	go func() { exited <- serve(ctx, serverTransport, &stderr, emptyEnv) }()

	client := sdk.NewClient(&sdk.Implementation{Name: "forjad-test-client", Version: "test"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	res, err := cs.CallTool(ctx, &sdk.CallToolParams{
		Name:      "forja.implement",
		Arguments: map[string]any{"repository": "fuseone-io/fuseone-forja", "issue": 12, "base_branch": "main"},
	})
	if err != nil {
		t.Fatalf("CallTool(forja.implement): protocol error %v", err)
	}
	if !res.IsError {
		t.Fatalf("forja.implement succeeded with no credentials: %v", res.StructuredContent)
	}
	text := ""
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			text += tc.Text
		}
	}
	if !strings.Contains(text, "FORJA_GITHUB_TOKEN") || strings.Contains(text, "not wired") {
		t.Errorf("error text %q must name the credentials, not a missing dependency", text)
	}
	_ = cs.Close()
	cancel()
	if code := <-exited; code != 0 {
		t.Fatalf("serve = %d, want 0", code)
	}
}

func TestNewRunID_hexOfSixteenRandomBytes(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for range 64 {
		id := newRunID()
		if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
			t.Fatalf("newRunID() = %q, want 32 lowercase hex characters", id)
		}
		if seen[id] {
			t.Fatalf("newRunID() repeated %q", id)
		}
		seen[id] = true
	}
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
