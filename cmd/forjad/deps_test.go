package main

import (
	"context"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/workspace"
)

// Postgres runs for seconds before it listens, so a readiness the adapter
// did not hand on to the container would leave it not ready.
func TestContainer_startService_readinessReachesTheContainer(t *testing.T) {
	d, err := workspace.NewDocker()
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := d.Ping(ctx); err != nil {
		t.Skipf("no Docker daemon: %v", err)
	}
	ws, err := launcher{d}.Launch(ctx, "alpine:3.20", t.TempDir())
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
