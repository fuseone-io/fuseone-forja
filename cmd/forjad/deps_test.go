package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/gates"
	"github.com/fuseone-io/fuseone-forja/internal/github"
	"github.com/fuseone-io/fuseone-forja/internal/workspace"
)

// linuxForjad cross-compiles this binary for the daemon's architecture, to
// serve as the run's gateway.
func linuxForjad(ctx context.Context, t *testing.T) string {
	t.Helper()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	defer func() { _ = cli.Close() }()
	v, err := cli.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		t.Fatalf("ServerVersion: %v", err)
	}
	path := filepath.Join(t.TempDir(), "forjad")
	build := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", path, "github.com/fuseone-io/fuseone-forja/cmd/forjad")
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+v.Arch, "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build forjad for linux/%s: %v\n%s", v.Arch, err, out)
	}
	return path
}

const wireKey = "sk-ant-wire-test-key-77aa"

// agentEnv is a complete agent configuration with credentials, with
// overrides applied; an empty override unsets the variable.
func agentEnv(t *testing.T, overrides map[string]string) func(string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"FORJA_GITHUB_TOKEN": "github_pat_dev", "FORJA_MODEL_API_KEY": wireKey,
		"FORJA_MODEL_TOKEN_CAP": "500000", "FORJA_AGENT_BINARY": binary,
	}
	for k, v := range overrides {
		env[k] = v
	}
	return func(k string) string { return env[k] }
}

// A key without a usable cap, upstream or agent would serve a tool that
// fails on its first run: the server does not start, and says which
// variable, never what the key is.
func TestWire_modelKeyWithBadCompanion_startupErrorNamingVariable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, variable, value string
	}{
		{name: "cap unset", variable: "FORJA_MODEL_TOKEN_CAP", value: ""},
		{name: "cap not a number", variable: "FORJA_MODEL_TOKEN_CAP", value: "lots"},
		{name: "cap zero", variable: "FORJA_MODEL_TOKEN_CAP", value: "0"},
		{name: "cap negative", variable: "FORJA_MODEL_TOKEN_CAP", value: "-5"},
		{name: "upstream with a path", variable: "FORJA_MODEL_UPSTREAM", value: "https://api.anthropic.com/v1"},
		{name: "agent unset", variable: "FORJA_AGENT_BINARY", value: ""},
		{name: "agent missing", variable: "FORJA_AGENT_BINARY", value: filepath.Join(t.TempDir(), "no-claude")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			getenv := agentEnv(t, map[string]string{tt.variable: tt.value})
			var stderr bytes.Buffer

			_, _, err := wire(getenv, &stderr)
			if err == nil || !strings.Contains(err.Error(), tt.variable) {
				t.Fatalf("wire = %v, want a startup error naming %s", err, tt.variable)
			}
			if strings.Contains(err.Error(), wireKey) || strings.Contains(stderr.String(), wireKey) {
				t.Fatalf("the key's value leaked: error %q, stderr %q", err, stderr.String())
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, serverTransport := sdk.NewInMemoryTransports()
			stderr.Reset()
			if code := serve(ctx, serverTransport, &stderr, getenv); code != 1 || strings.Contains(stderr.String(), wireKey) {
				t.Fatalf("serve = %d, stderr %q; want 1 without the key", code, stderr.String())
			}
		})
	}
}

// With the agent configured, forja.implement has an agent; without a key
// it has none and says what to set.
func TestWire_modelKey_decidesTheAgent(t *testing.T) {
	t.Parallel()
	deps, _, err := wire(agentEnv(t, nil), io.Discard)
	if err != nil || deps.Agent == nil {
		t.Fatalf("wire with the agent configured = agent %v, %v; want an agent", deps.Agent, err)
	}
	deps, _, err = wire(agentEnv(t, map[string]string{"FORJA_MODEL_API_KEY": "", "FORJA_MODEL_TOKEN_CAP": "", "FORJA_AGENT_BINARY": ""}), io.Discard)
	if err != nil || deps.Agent != nil {
		t.Fatalf("wire without a key = agent %v, %v; want none and no error", deps.Agent, err)
	}
}

// With a credential, a green run is delivered by the control plane's own
// GitHub clients; without one, each delivery call answers with the
// missing credential.
func TestWire_delivery_controlPlaneClientsOrTheMissingCredential(t *testing.T) {
	t.Parallel()
	deps, _, err := wire(agentEnv(t, nil), io.Discard)
	if err != nil {
		t.Fatalf("wire: %v", err)
	}
	if _, ok := deps.Pusher.(*github.Pusher); !ok {
		t.Errorf("Pusher = %T, want *github.Pusher", deps.Pusher)
	}
	if _, ok := deps.PullRequests.(*github.PullRequests); !ok {
		t.Errorf("PullRequests = %T, want *github.PullRequests", deps.PullRequests)
	}
	if _, ok := deps.Statuses.(*github.CommitStatuses); !ok {
		t.Errorf("Statuses = %T, want *github.CommitStatuses", deps.Statuses)
	}
	bare, _, err := wire(func(string) string { return "" }, io.Discard)
	if err != nil {
		t.Fatalf("wire without credentials: %v", err)
	}
	ctx := context.Background()
	errs := []error{
		bare.Pusher.Push(ctx, github.PushRequest{}),
		func() error { _, err := bare.PullRequests.Open(ctx, github.PullRequestRequest{}); return err }(),
		bare.Statuses.Publish(ctx, github.StatusRequest{}),
	}
	for i, err := range errs {
		if !errors.Is(err, github.ErrNoCredentials) {
			t.Errorf("delivery call %d without credentials = %v, want ErrNoCredentials", i, err)
		}
	}
}

// With a credential, a correction round clones the branch with the same
// cloner as forja.plan and knows Forja's login from that credential;
// without one, each of its calls answers with the missing credential.
func TestWire_round_branchClonerAndIdentityOrTheMissingCredential(t *testing.T) {
	t.Parallel()
	deps, _, err := wire(agentEnv(t, nil), io.Discard)
	if err != nil {
		t.Fatalf("wire: %v", err)
	}
	if c, ok := deps.BranchCloner.(cloner); !ok || c.Cloner == nil {
		t.Errorf("BranchCloner = %T, want the cloner adapter over *github.Cloner", deps.BranchCloner)
	}
	if id, ok := deps.Identity.(*github.StaticTokenSource); !ok || id.Value != "github_pat_dev" {
		t.Errorf("Identity = %T, want the PAT's own token source", deps.Identity)
	}
	bare, _, err := wire(func(string) string { return "" }, io.Discard)
	if err != nil {
		t.Fatalf("wire without credentials: %v", err)
	}
	ctx := context.Background()
	errs := map[string]error{
		"clone branch": func() error { _, err := bare.BranchCloner.CloneBranch(ctx, "o/r", "main", "forja/1"); return err }(),
		"login":        func() error { _, err := bare.Identity.Login(ctx); return err }(),
		"find":         func() error { _, err := bare.PullRequests.Find(ctx, "o/r", "forja/1"); return err }(),
		"comment":      bare.PullRequests.Comment(ctx, github.CommentRequest{}),
	}
	for name, err := range errs {
		if !errors.Is(err, github.ErrNoCredentials) {
			t.Errorf("%s without credentials = %v, want ErrNoCredentials", name, err)
		}
	}
}

// opaqueTokens is a token source that cannot say whose login it acts as.
type opaqueTokens struct{}

func (opaqueTokens) Token(context.Context, string, github.Permissions) (github.Token, error) {
	return github.Token{}, nil
}

// A credential whose login cannot be read would serve rounds that refuse
// every branch: forjad does not start.
func TestIdentityOf_tokenSourceWithoutLogin_startupError(t *testing.T) {
	t.Parallel()
	if _, err := identityOf(opaqueTokens{}); err == nil {
		t.Fatal("identityOf a token source without Login = nil error, want a startup failure")
	}
	if id, err := identityOf(&github.StaticTokenSource{Value: "x"}); err != nil || id == nil {
		t.Fatalf("identityOf the PAT source = %v, %v; want it", id, err)
	}
}

// Postgres runs for seconds before it listens, so a readiness the adapter
// did not hand on to the container would leave it not ready.
func TestContainer_startService_readinessReachesTheContainer(t *testing.T) {
	if os.Getenv("FORJA_DOCKER_TEST") != "1" {
		t.Skip("set FORJA_DOCKER_TEST=1 (make docker-test) to run the tests against the Docker Engine")
	}
	probe, err := workspace.NewDocker(workspace.Options{})
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := probe.Ping(ctx); err != nil {
		t.Fatalf("Docker tests requested but no daemon answered: %v", err)
	}
	d, err := workspace.NewDocker(workspace.Options{GatewayBinary: linuxForjad(ctx, t)})
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	var id [8]byte
	_, _ = rand.Read(id[:])
	ws, err := launcher{d}.Launch(ctx, gates.LaunchSpec{RunID: "deps-test-" + hex.EncodeToString(id[:]), Image: "alpine:3.20", Tree: t.TempDir()})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer closeCancel()
		_ = ws.Close(closeCtx)
	})

	svc := domain.Service{Image: "postgres:16", Env: map[string]string{"POSTGRES_HOST_AUTH_METHOD": "trust"}}
	res, err := ws.StartService(ctx, "postgres", svc, time.Minute)
	if err != nil || !res.Ready {
		t.Fatalf("StartService = ({ready %v, reason %q}, %v), want ready within a minute", res.Ready, res.Reason, err)
	}
}
