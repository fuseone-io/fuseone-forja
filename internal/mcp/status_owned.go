package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// ownedRuns is the store as one client sees it: only the runs recorded
// under owner, by id, as a cursor and in a list. Another client's run and
// one the store does not know are the same error, built here so that no
// store's wording can tell them apart.
type ownedRuns struct {
	runs  RunReader
	owner string
}

func (o ownedRuns) Get(ctx context.Context, id string) (domain.Run, error) {
	run, err := o.runs.Get(ctx, id)
	if errors.Is(err, domain.ErrRunNotFound) || (err == nil && (o.owner == "" || run.Client != o.owner)) {
		return domain.Run{}, fmt.Errorf("run %q: %w", id, domain.ErrRunNotFound)
	}
	return run, err
}

func (o ownedRuns) List(ctx context.Context, f domain.RunFilter) ([]domain.Run, error) {
	if o.owner == "" {
		return nil, errNoClient
	}
	f.Client = o.owner
	return o.runs.List(ctx, f)
}
