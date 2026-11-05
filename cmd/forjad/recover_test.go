package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/claim"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/github"
	"github.com/fuseone-io/fuseone-forja/internal/mcp"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
	"github.com/fuseone-io/fuseone-forja/internal/runstore/pgtest"
)

// claimRefs holds refs the way the GitHub API does for the calls recovery
// makes: reading a ref that is not there and deleting one that is not
// there are both ErrRefMissing.
type claimRefs struct {
	mu   sync.Mutex
	refs map[string]string
}

func (r *claimRefs) key(repo, ref string) string { return repo + " " + ref }

func (r *claimRefs) GetRef(_ context.Context, repo, ref string) (github.RefObject, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sha, ok := r.refs[r.key(repo, ref)]
	if !ok {
		return github.RefObject{}, github.ErrRefMissing
	}
	return github.RefObject{SHA: sha, Type: "commit"}, nil
}

func (r *claimRefs) DeleteRef(_ context.Context, repo, ref string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.refs[r.key(repo, ref)]; !ok {
		return github.ErrRefMissing
	}
	delete(r.refs, r.key(repo, ref))
	return nil
}

var errNotRecovery = errors.New("not a call recovery makes")

func (r *claimRefs) CreateRef(context.Context, string, string, string) error { return errNotRecovery }
func (r *claimRefs) UpdateRef(context.Context, string, string, string) error { return errNotRecovery }
func (r *claimRefs) CommitFile(context.Context, string, github.FileCommit) (string, error) {
	return "", errNotRecovery
}
func (r *claimRefs) ReadFile(context.Context, string, string, string) ([]byte, error) {
	return nil, errNotRecovery
}

const crashedRepo = "acme/api"

// crashedClaim is a claim store whose journal still holds the claim of
// run-crashed on issue 7, what a process that died mid-run leaves, over a
// repository whose claim ref points at refSHA; none when refSHA is empty.
func crashedClaim(t *testing.T, refSHA string) (*claim.Store, *claimRefs) {
	t.Helper()
	journal := &claim.Journal{Dir: filepath.Join(t.TempDir(), "claims")}
	if err := journal.Put(claim.Entry{Repository: crashedRepo, Issue: 7, RunID: "run-crashed", SHA: "c1a1"}); err != nil {
		t.Fatalf("journal: %v", err)
	}
	refs := &claimRefs{refs: map[string]string{}}
	if refSHA != "" {
		refs.refs[crashedRepo+" "+domain.ClaimRef(7)] = refSHA
	}
	return &claim.Store{Refs: refs, Now: time.Now, Journal: journal}, refs
}

var recoveredAt = time.Date(2026, 11, 5, 9, 30, 0, 0, time.UTC)

func runningRun() domain.Run {
	start := recoveredAt.Add(-time.Hour)
	return domain.Run{
		ID: "run-crashed", Tool: domain.ToolImplement, Repository: crashedRepo, Issue: 7,
		State: domain.StateRunning, StartedAt: start,
		Stages: []domain.Stage{{Name: domain.StageAgent, StartedAt: start, Outcome: domain.StageRunning, Attempt: 1}},
	}
}

// storesUnderRecovery is the memory store, and Postgres when the Docker
// tests run.
func storesUnderRecovery(t *testing.T) map[string]func(t *testing.T) mcp.RunStore {
	t.Helper()
	stores := map[string]func(t *testing.T) mcp.RunStore{
		"memory": func(*testing.T) mcp.RunStore { return runstore.NewMemory() },
	}
	if dockerTests() {
		srv := pgtest.Start(t)
		stores["postgres"] = func(t *testing.T) mcp.RunStore { return migratedRuns(t, srv.Database(t)) }
	}
	return stores
}

// The claim a crash left behind is released on start, and the run that
// held it, still running in the store, ends failed as interrupted at the
// new process's clock, with its open stage closed.
func TestRecoverClaims_crashedRunningRun_endsFailedInterrupted(t *testing.T) {
	t.Parallel()
	for name, open := range storesUnderRecovery(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			runs := open(t)
			if err := runs.Put(ctx, runningRun()); err != nil {
				t.Fatalf("Put: %v", err)
			}
			deps := mcp.Deps{Runs: runs, Now: func() time.Time { return recoveredAt }}
			var stderr bytes.Buffer
			claims, refs := crashedClaim(t, "c1a1")

			if err := recoverClaims(ctx, claims, deps, &stderr); err != nil {
				t.Fatalf("recoverClaims: %v", err)
			}
			if _, err := refs.GetRef(ctx, crashedRepo, domain.ClaimRef(7)); !errors.Is(err, github.ErrRefMissing) {
				t.Fatalf("claim ref after recovery: %v, want it released", err)
			}

			got, err := runs.Get(ctx, "run-crashed")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			at := recoveredAt
			want := runningRun()
			want.State, want.Reason, want.FinishedAt = domain.StateFailed, domain.ReasonInterrupted, &at
			want.Stages[0].Outcome, want.Stages[0].FinishedAt = domain.StageFailed, &at
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("recovered run = %+v, want %+v", got, want)
			}
			if !strings.Contains(stderr.String(), "run-crashed") || !strings.Contains(stderr.String(), "interrupted") {
				t.Fatalf("stderr %q does not report the run closed as interrupted", stderr.String())
			}
		})
	}
}

// A run that had already ended before the crash keeps its outcome; one the
// store never had, such as every run of an in-memory store after a
// restart, is no failure.
func TestRecoverClaims_runEndedOrUnknown_leftAsIs(t *testing.T) {
	t.Parallel()
	finished := recoveredAt.Add(-time.Minute)
	ended := runningRun()
	ended.State, ended.FinishedAt, ended.Reason = domain.StateFailed, &finished, domain.ReasonGateFailed
	tests := map[string][]domain.Run{"ended": {ended}, "unknown": nil}
	for name, seed := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			runs := runstore.NewMemory()
			for _, r := range seed {
				if err := runs.Put(ctx, r); err != nil {
					t.Fatalf("Put: %v", err)
				}
			}
			deps := mcp.Deps{Runs: runs, Now: func() time.Time { return recoveredAt }}
			var stderr bytes.Buffer
			claims, _ := crashedClaim(t, "c1a1")
			if err := recoverClaims(ctx, claims, deps, &stderr); err != nil {
				t.Fatalf("recoverClaims: %v", err)
			}
			got, err := runs.List(ctx, domain.RunFilter{})
			if err != nil || !reflect.DeepEqual(got, append([]domain.Run{}, seed...)) {
				t.Fatalf("store after recovery = %+v (%v), want %+v", got, err, seed)
			}
			if strings.Contains(stderr.String(), "interrupted") {
				t.Fatalf("stderr %q reports a run closed that was not", stderr.String())
			}
		})
	}
}

// brokenRuns is a store that answers neither reads nor writes.
type brokenRuns struct {
	*runstore.Memory
	getErr, putErr error
}

func (b brokenRuns) Get(ctx context.Context, id string) (domain.Run, error) {
	if b.getErr != nil {
		return domain.Run{}, b.getErr
	}
	return b.Memory.Get(ctx, id)
}

func (b brokenRuns) Put(ctx context.Context, run domain.Run) error {
	if b.putErr != nil {
		return b.putErr
	}
	return b.Memory.Put(ctx, run)
}

// A store that cannot say what the crashed run was, or cannot record it
// ended, stops the start before the claim is released: serving on would
// leave the run running, and the next start finds the journal as it was.
func TestRecoverClaims_storeFails_startupErrorNamingTheRun(t *testing.T) {
	t.Parallel()
	errStore := errors.New("store down")
	tests := map[string]brokenRuns{
		"read fails":  {getErr: errStore},
		"write fails": {putErr: errStore},
	}
	for name, runs := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			runs.Memory = runstore.NewMemory()
			if err := runs.Memory.Put(ctx, runningRun()); err != nil {
				t.Fatalf("Put: %v", err)
			}
			deps := mcp.Deps{Runs: runs, Now: func() time.Time { return recoveredAt }}
			claims, refs := crashedClaim(t, "c1a1")
			err := recoverClaims(ctx, claims, deps, &bytes.Buffer{})
			if !errors.Is(err, errStore) || !strings.Contains(err.Error(), "run-crashed") {
				t.Fatalf("recoverClaims = %v, want the store's error naming run-crashed", err)
			}
			entries, jerr := claims.Journal.List()
			if _, rerr := refs.GetRef(ctx, crashedRepo, domain.ClaimRef(7)); rerr != nil || jerr != nil || len(entries) != 1 {
				t.Fatalf("after the failure: ref %v, journal %v (%v); want both kept for the next start", rerr, entries, jerr)
			}
		})
	}
}

// A journaled claim whose ref vanished or moved to another run is still a
// run of the process that died: the run is closed, the ref left alone.
func TestRecoverClaims_refNotOurs_runStillClosedRefUntouched(t *testing.T) {
	t.Parallel()
	tests := map[string]string{"ref vanished": "", "ref moved on": "b2b2"}
	for name, refSHA := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			runs := runstore.NewMemory()
			if err := runs.Put(ctx, runningRun()); err != nil {
				t.Fatalf("Put: %v", err)
			}
			deps := mcp.Deps{Runs: runs, Now: func() time.Time { return recoveredAt }}
			claims, refs := crashedClaim(t, refSHA)
			if err := recoverClaims(ctx, claims, deps, &bytes.Buffer{}); err != nil {
				t.Fatalf("recoverClaims: %v", err)
			}
			got, err := runs.Get(ctx, "run-crashed")
			if err != nil || got.State != domain.StateFailed || got.Reason != domain.ReasonInterrupted {
				t.Fatalf("run after recovery = %q %q (%v), want failed interrupted", got.State, got.Reason, err)
			}
			obj, err := refs.GetRef(ctx, crashedRepo, domain.ClaimRef(7))
			if refSHA != "" && (err != nil || obj.SHA != refSHA) {
				t.Fatalf("the other run's ref = %+v (%v), want it untouched at %s", obj, err, refSHA)
			}
		})
	}
}
