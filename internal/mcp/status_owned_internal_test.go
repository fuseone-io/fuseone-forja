package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

// A context that names no client sees no run, by id or in a list, even
// one the store holds: the scope fails closed when no caller was named.
func TestStatus_noClientOnTheContext_noRunAnswered(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := runstore.NewMemory()
	run := domain.Run{ID: "run-1", Tool: domain.ToolDoctor, Repository: "acme/api", Client: "client-a", State: domain.StateRunning, StartedAt: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
	if err := store.Put(ctx, run); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := status(ctx, store, nil, StatusInput{RunID: "run-1"}); !errors.Is(err, domain.ErrRunNotFound) {
		t.Fatalf("by id with no client = %v, want not found", err)
	}
	if out, err := status(ctx, store, nil, StatusInput{}); !errors.Is(err, errNoClient) {
		t.Fatalf("list with no client = (%+v, %v), want errNoClient", out.List, err)
	}
	if _, err := status(ctx, store, nil, StatusInput{Cursor: "run-1"}); !errors.Is(err, errNoClient) {
		t.Fatalf("cursor with no client = %v, want errNoClient", err)
	}
}

// unownedReader is a store holding a run recorded under no client, as a
// store that does not validate its rows could hand one back.
type unownedReader struct{}

func (unownedReader) Get(context.Context, string) (domain.Run, error) {
	return domain.Run{ID: "run-unowned", Tool: domain.ToolDoctor, State: domain.StateSucceeded}, nil
}

func (unownedReader) List(context.Context, domain.RunFilter) ([]domain.Run, error) {
	return []domain.Run{}, nil
}

// A run recorded under no client is no one's, not the run of a call that
// names no client.
func TestStatus_unownedRun_answeredToNoOne(t *testing.T) {
	t.Parallel()
	for _, owner := range []string{"", "client-a"} {
		ctx := withClient(context.Background(), domain.Client{Name: owner})
		if _, err := status(ctx, unownedReader{}, nil, StatusInput{RunID: "run-unowned"}); !errors.Is(err, domain.ErrRunNotFound) {
			t.Fatalf("client %q: an unowned run = %v, want not found", owner, err)
		}
	}
}
