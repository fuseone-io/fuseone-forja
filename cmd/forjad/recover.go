package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/fuseone-io/fuseone-forja/internal/claim"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/mcp"
)

// recoverClaims closes in deps.Runs the run of every claim a previous
// process journaled, then releases those claims, and reports both on
// stderr by repository, issue and run id. The journal holds only claims
// of a process that stopped: this one has run nothing yet. A run no claim
// names is left alone, since another live process may be running it. The
// runs are closed first so that a store that fails leaves the journal for
// the next start.
func recoverClaims(ctx context.Context, store *claim.Store, deps mcp.Deps, stderr io.Writer) error {
	if store == nil {
		return nil
	}
	if err := interruptJournaled(ctx, store.Journal, deps, stderr); err != nil {
		return fmt.Errorf("recover claims: %w", err)
	}
	released, err := store.Recover(ctx)
	for _, c := range released {
		// The exit code is the only channel left when stderr itself fails.
		_, _ = fmt.Fprintf(stderr, "forjad: released the claim of issue %d of %s left by run %s\n", c.Issue, c.Repository, c.RunID)
	}
	if err != nil {
		return fmt.Errorf("recover claims: %w", err)
	}
	return nil
}

// interruptJournaled closes the run of every journaled claim, whether or
// not its ref is still the claim's.
func interruptJournaled(ctx context.Context, journal *claim.Journal, deps mcp.Deps, stderr io.Writer) error {
	if journal == nil {
		return nil
	}
	entries, err := journal.List()
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := interruptRun(ctx, deps, e.RunID, stderr); err != nil {
			return err
		}
	}
	return nil
}

// interruptRun records as interrupted the run id when the store still has
// it in flight. A run the store never had, as in a store kept in memory,
// is nothing to close.
func interruptRun(ctx context.Context, deps mcp.Deps, id string, stderr io.Writer) error {
	run, err := deps.Runs.Get(ctx, id)
	if errors.Is(err, domain.ErrRunNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("close run %s: %w", id, err)
	}
	run, changed := run.Interrupted(deps.Now())
	if !changed {
		return nil
	}
	if err := deps.Runs.Put(ctx, run); err != nil {
		return fmt.Errorf("close run %s: %w", id, err)
	}
	// The exit code is the only channel left when stderr itself fails.
	_, _ = fmt.Fprintf(stderr, "forjad: run %s ended failed: %s\n", id, domain.ReasonInterrupted)
	return nil
}
