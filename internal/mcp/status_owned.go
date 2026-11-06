package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// ownedRuns is the store as one client sees it: only the runs recorded
// under owner, by id, as a cursor and in a list. The store reads only the
// owner's rows, so a failure is the owner's own and is passed on. Another
// client's run and one the store does not know are the same error, built
// here so that no store's wording can tell them apart.
type ownedRuns struct {
	runs  RunReader
	owner string
}

func (o ownedRuns) Get(ctx context.Context, id string) (domain.Run, error) {
	if o.owner == "" {
		return domain.Run{}, fmt.Errorf("run %q: %w", id, domain.ErrRunNotFound)
	}
	run, err := o.runs.GetOwned(ctx, o.owner, id)
	if errors.Is(err, domain.ErrRunNotFound) || (err == nil && run.Client != o.owner) {
		return domain.Run{}, fmt.Errorf("run %q: %w", id, domain.ErrRunNotFound)
	}
	return run, err
}

// owns reports whether the run id is recorded under the owner. A run of
// another client, one the store does not know and one the store fails to
// read are all not the owner's: a response names a run only when the
// store vouches for it.
func (o ownedRuns) owns(ctx context.Context, id string) bool {
	if id == "" {
		return false
	}
	_, err := o.Get(ctx, id)
	return err == nil
}

// supersedes is the run id the owner may read as the run its run took
// over: id when the owner owns that run too, empty otherwise, as if the
// issue had been free.
func (o ownedRuns) supersedes(ctx context.Context, id string) string {
	if !o.owns(ctx, id) {
		return ""
	}
	return id
}

func (o ownedRuns) List(ctx context.Context, f domain.RunFilter) ([]domain.Run, error) {
	if o.owner == "" {
		return nil, errNoClient
	}
	f.Client = o.owner
	return o.runs.List(ctx, f)
}
