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

// countingClaims counts every claim it is asked for and refuses them all.
type countingClaims struct{ claims int }

func (c *countingClaims) Claim(context.Context, domain.ClaimRequest) (domain.Claim, error) {
	c.claims++
	return domain.Claim{}, errors.New("countingClaims: refused")
}

func (c *countingClaims) Release(context.Context, domain.Claim) error { return nil }

// A run reached with no client on its context claims nothing: the claim
// ref would otherwise be created for a run no store can record.
func TestLifecycle_noClientOnTheContext_noClaimAttempted(t *testing.T) {
	t.Parallel()
	claims := &countingClaims{}
	body := []byte("version: 1\nimage: golang:1.25\ngates:\n  - name: build\n    run: make build\n")
	l := lifecycle{deps: Deps{Claims: claims, Contracts: fixedContracts{body: body}, NewID: func() string { return "r" }, Now: time.Now}}
	_, err := l.claim(context.Background(), runTarget{tool: domain.ToolImplement, repo: "acme/api", issue: 3})
	if !errors.Is(err, errNoClient) || claims.claims != 0 {
		t.Fatalf("claim = %v after %d claims, want errNoClient and none", err, claims.claims)
	}
	_, err = l.claim(withClient(context.Background(), domain.Client{Name: "client-a"}), runTarget{tool: domain.ToolImplement, repo: "acme/api", issue: 3})
	if errors.Is(err, errNoClient) || claims.claims != 1 {
		t.Fatalf("claim with a client = %v after %d claims, want the claim tried once", err, claims.claims)
	}
}

// countingCloner counts the clones it is asked for and refuses them all.
type countingCloner struct{ clones int }

func (c *countingCloner) Clone(context.Context, string, string) (Checkout, error) {
	c.clones++
	return nil, errors.New("countingCloner: refused")
}

// A doctor run reached with no client on its context clones nothing.
func TestDoctor_noClientOnTheContext_noClone(t *testing.T) {
	t.Parallel()
	cloner := &countingCloner{}
	deps := Deps{Cloner: cloner, Gates: greenGates{}, Runs: runstore.NewMemory(), NewID: func() string { return "r" }, Now: time.Now}
	if _, err := doctor(context.Background(), deps, "acme/api"); !errors.Is(err, errNoClient) || cloner.clones != 0 {
		t.Fatalf("doctor = %v after %d clones, want errNoClient and none", err, cloner.clones)
	}
	if _, err := doctor(withClient(context.Background(), domain.Client{Name: "client-a"}), deps, "acme/api"); errors.Is(err, errNoClient) || cloner.clones != 1 {
		t.Fatalf("doctor with a client = %v after %d clones, want the clone tried once", err, cloner.clones)
	}
}

// failingReader fails every read of the caller's own runs with err, as a
// durable store does on a row it cannot decode or a database it cannot
// reach, and records which client it was asked for.
type failingReader struct {
	err   error
	asked *[]string
}

func (f failingReader) Get(context.Context, string) (domain.Run, error) {
	return domain.Run{}, errors.New("failingReader: Get reads any client's row")
}

func (f failingReader) GetOwned(_ context.Context, client, _ string) (domain.Run, error) {
	*f.asked = append(*f.asked, client)
	return domain.Run{}, f.err
}

func (failingReader) List(context.Context, domain.RunFilter) ([]domain.Run, error) {
	return []domain.Run{}, nil
}

// forja.status reads a run by id and as a cursor only as the caller's own,
// so the store never touches another client's row; a failure on the
// caller's own run is then the caller's to see, not an unknown id.
func TestStatus_byIDAndCursor_readOnlyAsTheCallersOwnFailurePassedOn(t *testing.T) {
	t.Parallel()
	ctx := withClient(context.Background(), domain.Client{Name: "client-b"})
	down := errors.New("connection refused")
	for _, in := range []StatusInput{{RunID: "run-x"}, {Cursor: "run-x"}} {
		var asked []string
		_, err := status(ctx, failingReader{err: down, asked: &asked}, nil, in)
		if !errors.Is(err, down) || errors.Is(err, domain.ErrRunNotFound) {
			t.Fatalf("%+v: = %v, want the store's failure", in, err)
		}
		if len(asked) != 1 || asked[0] != "client-b" {
			t.Fatalf("%+v: the store was asked for %q, want only client-b's runs", in, asked)
		}
	}
}

// unownedReader is a store holding a run recorded under no client, as a
// store that does not validate its rows could hand one back.
type unownedReader struct{}

func (unownedReader) Get(context.Context, string) (domain.Run, error) {
	return domain.Run{ID: "run-unowned", Tool: domain.ToolDoctor, State: domain.StateSucceeded}, nil
}

func (unownedReader) GetOwned(context.Context, string, string) (domain.Run, error) {
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
