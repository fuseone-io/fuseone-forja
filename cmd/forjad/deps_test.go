package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/gates"
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
