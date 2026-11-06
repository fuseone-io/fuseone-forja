package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/mcp"
)

// errRunStoreUnavailable is all a tool's caller learns of a run store
// failure.
var errRunStoreUnavailable = errors.New("run store unavailable")

// sealedRuns is the run store as the tools see it: a failure is answered
// errRunStoreUnavailable and its cause goes to the operator's log, since a
// driver's error names the database's address and user. A run the store
// does not find, or one recorded under another client, is the store's own
// answer and is passed on as it is; a call whose own context ended is
// answered that context's error.
type sealedRuns struct {
	runs mcp.RunStore
	log  io.Writer
}

// sealRuns seals runs, logging every sealed cause on log.
func sealRuns(runs mcp.RunStore, log io.Writer) sealedRuns {
	return sealedRuns{runs: runs, log: log}
}

func (s sealedRuns) Get(ctx context.Context, id string) (domain.Run, error) {
	run, err := s.runs.Get(ctx, id)
	return run, s.seal(ctx, "get", err)
}

func (s sealedRuns) GetOwned(ctx context.Context, client, id string) (domain.Run, error) {
	run, err := s.runs.GetOwned(ctx, client, id)
	return run, s.seal(ctx, "get", err)
}

func (s sealedRuns) List(ctx context.Context, f domain.RunFilter) ([]domain.Run, error) {
	runs, err := s.runs.List(ctx, f)
	return runs, s.seal(ctx, "list", err)
}

func (s sealedRuns) Put(ctx context.Context, run domain.Run) error {
	return s.seal(ctx, "put", s.runs.Put(ctx, run))
}

func (s sealedRuns) seal(ctx context.Context, op string, err error) error {
	if err == nil || errors.Is(err, domain.ErrRunNotFound) || errors.Is(err, domain.ErrRunClientChanged) {
		return err
	}
	if ended := ctx.Err(); ended != nil {
		return ended
	}
	// A log that cannot be written leaves the caller's answer as it is.
	_, _ = fmt.Fprintf(s.log, "forjad: run store: %s: %v\n", op, err)
	return errRunStoreUnavailable
}
