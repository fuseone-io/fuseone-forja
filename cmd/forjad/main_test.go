package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
			if code := run(tt.args, io.Discard, &stderr); code != 2 {
				t.Fatalf("run(%q) = %d, want 2", tt.args, code)
			}
			if !strings.Contains(stderr.String(), "usage: forjad serve") {
				t.Fatalf("stderr %q does not print the usage", stderr.String())
			}
		})
	}
}

func TestRun_gatewayMisconfigured_exit2NamingTheProblem(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "no run id", args: []string{"gateway", "-allow", "proxy.golang.org:443"}, want: "-run"},
		{name: "malformed allow entry", args: []string{"gateway", "-run", "r1", "-allow", "https://proxy.golang.org"}, want: "https://proxy.golang.org"},
		{name: "extra argument", args: []string{"gateway", "-run", "r1", "extra"}, want: "usage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			if code := run(tt.args, &stdout, &stderr); code != 2 {
				t.Fatalf("run(%q) = %d, want 2", tt.args, code)
			}
			if !strings.Contains(stderr.String(), tt.want) {
				t.Errorf("stderr %q does not name %q", stderr.String(), tt.want)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want nothing: a gateway that did not start is never ready", stdout.String())
			}
		})
	}
}

// The subcommand is the gateway the workspace container is given: it says
// it is ready on stdout, refuses what is not listed with one line there,
// and exits 0 when stopped.
func TestGatewayCommand_readyThenDenialLineThenCleanStop(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdoutR, stdoutW := io.Pipe()
	var stderr bytes.Buffer
	exited := make(chan int, 1)
	go func() {
		exited <- gateway(ctx, []string{"-run", "run-7", "-listen", "127.0.0.1:0", "-allow", "proxy.golang.org:443"}, stdoutW, &stderr)
		_ = stdoutW.Close()
	}()
	lines := bufio.NewScanner(stdoutR)

	addr := readyAddr(t, lines)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := io.WriteString(conn, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	if !lines.Scan() || !strings.Contains(lines.Text(), `"destination":"example.com:443"`) || !strings.Contains(lines.Text(), `"run":"run-7"`) {
		t.Fatalf("line after the CONNECT = %q, want the denial of example.com:443 for run-7", lines.Text())
	}
	cancel()
	go func() { _, _ = io.Copy(io.Discard, stdoutR) }()
	if code := <-exited; code != 0 {
		t.Fatalf("gateway after cancel = %d, want 0; stderr %q", code, stderr.String())
	}
}

// readyAddr reads the readiness line and returns the address it names.
func readyAddr(t *testing.T, lines *bufio.Scanner) string {
	t.Helper()
	if !lines.Scan() {
		t.Fatalf("no readiness line: %v", lines.Err())
	}
	var ready struct {
		Event string `json:"event"`
		Run   string `json:"run"`
		Addr  string `json:"addr"`
	}
	if err := json.Unmarshal(lines.Bytes(), &ready); err != nil || ready.Event != "ready" || ready.Run != "run-7" || ready.Addr == "" {
		t.Fatalf("first line = %q (%v), want the readiness of run-7 with its address", lines.Text(), err)
	}
	return ready.Addr
}

// registryCredentials writes a credentials file for the named entries.
func registryCredentials(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "registries.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRun_registryMisconfigured_exit2NothingOnStdout(t *testing.T) {
	t.Parallel()
	good := registryCredentials(t, `{"ghcr.io/my-org": {"username": "u", "password": "canary-cmd-secret"}}`)
	bad := registryCredentials(t, `{"ghcr.io/my-org": {"username": "u", "password": canary-cmd-secret}}`)
	tests := []struct {
		name string
		args []string
	}{
		{name: "unknown flag", args: []string{"registry", "-run", "r1", "-registries", "ghcr.io/my-org", "-credentials", good, "-nope"}},
		{name: "no run id", args: []string{"registry", "-registries", "ghcr.io/my-org", "-credentials", good}},
		{name: "no registry", args: []string{"registry", "-run", "r1", "-credentials", good}},
		{name: "malformed registry", args: []string{"registry", "-run", "r1", "-registries", "https://ghcr.io", "-credentials", good}},
		{name: "no credentials file", args: []string{"registry", "-run", "r1", "-registries", "ghcr.io/my-org"}},
		{name: "credentials file malformed", args: []string{"registry", "-run", "r1", "-registries", "ghcr.io/my-org", "-credentials", bad}},
		{name: "declared registry without credential", args: []string{"registry", "-run", "r1", "-registries", "ghcr.io/my-org,npm.pkg.github.com", "-credentials", good}},
		{name: "extra argument", args: []string{"registry", "-run", "r1", "-registries", "ghcr.io/my-org", "-credentials", good, "extra"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			if code := run(tt.args, &stdout, &stderr); code != 2 {
				t.Fatalf("run(%q) = %d, want 2; stderr %q", tt.args, code, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want nothing: a proxy that did not start is never ready", stdout.String())
			}
			if stderr.Len() == 0 || strings.Contains(stderr.String(), "canary-cmd-secret") {
				t.Errorf("stderr = %q, want the problem named and no credential", stderr.String())
			}
		})
	}
}

// The subcommand is the registry proxy the control plane starts: ready on
// stdout, one access line per request, exit 0 when stopped.
func TestRegistryCommand_readyThenAccessLineThenCleanStop(t *testing.T) {
	t.Parallel()
	creds := registryCredentials(t, `{"ghcr.io/my-org": {"username": "u", "password": "canary-cmd-secret"}}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdoutR, stdoutW := io.Pipe()
	var stderr bytes.Buffer
	exited := make(chan int, 1)
	go func() {
		exited <- registryProxy(ctx, []string{"-run", "run-7", "-listen", "127.0.0.1:0", "-registries", "ghcr.io/my-org", "-credentials", creds}, stdoutW, &stderr)
		_ = stdoutW.Close()
	}()
	lines := bufio.NewScanner(stdoutR)

	addr := readyAddr(t, lines)
	// The proxy writes the line before it answers, so the pipe is read
	// while the request is in flight.
	next := make(chan string, 1)
	go func() {
		if lines.Scan() {
			next <- lines.Text()
		}
		close(next)
		_, _ = io.Copy(io.Discard, stdoutR)
	}()
	res, err := http.Get("http://" + addr + "/example.com/x?q=1")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("GET of an undeclared registry = %d, want 403", res.StatusCode)
	}
	if text := <-next; !strings.Contains(text, `"registry":"example.com"`) || !strings.Contains(text, `"outcome":"denied"`) {
		t.Fatalf("line after the request = %q, want the denied access of example.com", text)
	}
	cancel()
	if code := <-exited; code != 0 {
		t.Fatalf("registry after cancel = %d, want 0; stderr %q", code, stderr.String())
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

// The configured lease is what the tools hold the issue for; unset, the
// contract's wall clock decides.
func TestWire_claimLease_configuredReachesTheTools(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "configured", value: "50m", want: 50 * time.Minute},
		{name: "unset", value: "", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := map[string]string{"FORJA_GITHUB_TOKEN": "github_pat_dev", "FORJA_CLAIM_LEASE": tt.value}
			var stderr bytes.Buffer

			deps, _, err := wire(func(k string) string { return env[k] }, &stderr)
			if err != nil {
				t.Fatalf("wire: %v", err)
			}
			if deps.Lease != tt.want {
				t.Fatalf("Deps.Lease = %v with FORJA_CLAIM_LEASE=%q, want %v", deps.Lease, tt.value, tt.want)
			}
		})
	}
}

// A malformed egress allowlist would either let out what the operator did
// not mean or nothing at all; it stops the server before it serves.
func TestWire_malformedEgressAllow_startupError(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"proxy.golang.org", "https://proxy.golang.org:443", "proxy.golang.org:443,,sum.golang.org:443"} {
		env := map[string]string{"FORJA_EGRESS_ALLOW": value}
		_, _, err := wire(func(k string) string { return env[k] }, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "FORJA_EGRESS_ALLOW") {
			t.Errorf("wire with FORJA_EGRESS_ALLOW=%q = %v, want a startup error naming the variable", value, err)
		}
	}
}

func TestServe_malformedEgressAllow_exitsOne(t *testing.T) {
	t.Parallel()
	env := map[string]string{"FORJA_EGRESS_ALLOW": "*.golang.org:443"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, serverTransport := sdk.NewInMemoryTransports()

	var stderr bytes.Buffer
	if code := serve(ctx, serverTransport, &stderr, func(k string) string { return env[k] }); code != 1 {
		t.Fatalf("serve with a malformed allowlist = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "FORJA_EGRESS_ALLOW") {
		t.Fatalf("stderr %q does not name the variable", stderr.String())
	}
}

func TestWire_wellFormedEgressAllow_starts(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		"FORJA_EGRESS_ALLOW":   "proxy.golang.org:443,sum.golang.org:443,storage.googleapis.com:443",
		"FORJA_GATEWAY_BINARY": "/opt/forja/forjad-linux",
	}
	if _, _, err := wire(func(k string) string { return env[k] }, io.Discard); err != nil {
		t.Fatalf("wire with a well-formed allowlist = %v", err)
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
