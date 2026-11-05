package main

import (
	"context"
	"fmt"
	"io"

	"github.com/fuseone-io/fuseone-forja/internal/claim"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/mcp"
)

// recoverClaims closes in deps.Runs every run a previous process left in
// flight, then releases the claims it left behind, and reports both on
// stderr by repository, issue and run id. On start this process has run
// nothing, and it is the only one on the store, so every queued or running
// run is a previous process's, claimed or not. The runs are closed first
// so that a store that fails leaves the journal for the next start.
func recoverClaims(ctx context.Context, store *claim.Store, deps mcp.Deps, stderr io.Writer) error {
	if err := interruptInFlight(ctx, deps, stderr); err != nil {
		return fmt.Errorf("recover runs: %w", err)
	}
	if store == nil {
		return nil
	}
	released, err := store.Recover(ctx)
	if err != nil {
		return fmt.Errorf("recover claims: %w", err)
	}
	for _, c := range released {
		// The exit code is the only channel left when stderr itself fails.
		_, _ = fmt.Fprintf(stderr, "forjad: released the claim of issue %d of %s left by run %s\n", c.Issue, c.Repository, c.RunID)
	}
	return nil
}

// interruptInFlight records as interrupted every queued and running run.
func interruptInFlight(ctx context.Context, deps mcp.Deps, stderr io.Writer) error {
	for _, state := range []domain.RunState{domain.StateQueued, domain.StateRunning} {
		runs, err := deps.Runs.List(ctx, domain.RunFilter{State: state})
		if err != nil {
			return err
		}
		for _, run := range runs {
			if err := interruptRun(ctx, deps, run, stderr); err != nil {
				return err
			}
		}
	}
	return nil
}

func interruptRun(ctx context.Context, deps mcp.Deps, run domain.Run, stderr io.Writer) error {
	closed, changed := run.Interrupted(deps.Now())
	if !changed {
		return nil
	}
	if err := deps.Runs.Put(ctx, closed); err != nil {
		return fmt.Errorf("close run %s: %w", run.ID, err)
	}
	// The exit code is the only channel left when stderr itself fails.
	_, _ = fmt.Fprintf(stderr, "forjad: run %s ended failed: %s\n", run.ID, domain.ReasonInterrupted)
	return nil
}
