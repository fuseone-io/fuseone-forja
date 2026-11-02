package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/agent"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/workspace"
)

// The driver's refusal of a run whose cap its earlier attempts spent
// reaches the runner in the runner's own terms: a capped result with no
// workspace and no error. The refusal comes before the daemon is asked
// anything.
func TestAgentRunner_capSpentByEarlierAttempts_cappedNoError(t *testing.T) {
	t.Parallel()
	binary := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := workspace.NewDocker(workspace.Options{AgentBinary: binary, Model: workspace.ModelOptions{Key: wireKey, Cap: 100}})
	if err != nil {
		t.Fatal(err)
	}

	res, err := agentRunner(d, true).Run(context.Background(), agent.Task{
		RunID: "run-capped", Contract: domain.Contract{Image: "golang:1.25"}, Tree: t.TempDir(),
		Spent: domain.TokenUsage{Input: 60, Output: 40},
	})
	if err != nil || !res.Capped || res.ExitCode != domain.NoExitCode || res.Exported {
		t.Fatalf("Run = %+v, %v; want capped with no exit code and no error", res, err)
	}
}
