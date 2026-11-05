package claim_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/claim"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// settle is what the run does with what Claim answered: a claim it holds
// is released on a context of its own; an error leaves it nothing to
// release.
func settle(t *testing.T, store *claim.Store, c domain.Claim, err error) {
	t.Helper()
	if err != nil {
		return
	}
	if rerr := store.Release(context.Background(), c); rerr != nil {
		t.Fatalf("Release of the claim Claim answered: %v", rerr)
	}
}

// assertNothingLeft fails when the ref or a journal entry outlives the
// claim's settling.
func assertNothingLeft(t *testing.T, fake *fakeRefs, journal *claim.Journal) {
	t.Helper()
	if sha, present := fake.refSHA(repo, ref); present {
		t.Fatalf("ref still points at %s: nothing releases it before its lease ends", sha)
	}
	if entries := mustList(t, journal); len(entries) != 0 {
		t.Fatalf("journal still holds %+v", entries)
	}
}

// The caller goes away while GitHub applies the ref's creation or move:
// the mutation completes and is journaled, so the claim Claim answers is
// released by the run, or it is refused with the ref given back. Either
// way no ref is left behind.
func TestStore_claim_callerGoneDuringRefMutation_noRefLeftBehind(t *testing.T) {
	t.Parallel()
	for _, op := range []string{"CreateRef", "UpdateRef"} {
		t.Run(op, func(t *testing.T) {
			t.Parallel()
			fake := newFakeRefs()
			journal := &claim.Journal{Dir: t.TempDir()}
			if op == "UpdateRef" {
				old, err := claim.NewStore(fake, clock(t0()), journal).Claim(context.Background(), request("run-old"))
				if err != nil {
					t.Fatalf("first Claim: %v", err)
				}
				if err := journal.Remove(old.Repository, old.Issue); err != nil {
					t.Fatalf("forget the expired claim: %v", err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			fake.cancelOn, fake.cancel = op, cancel
			store := claim.NewStore(fake, clock(t0().Add(lease)), journal)
			c, err := store.Claim(ctx, request("run-1"))
			if !slices.Contains(fake.callLog(), op) {
				t.Fatalf("calls %q: the %s under test was never made", fake.callLog(), op)
			}
			settle(t, store, c, err)
			assertNothingLeft(t, fake, journal)
		})
	}
}

// When the journal cannot record a claim whose creation or move the
// caller left during, the ref still comes back off.
func TestStore_claim_callerGoneAndJournalFails_refGivenBack(t *testing.T) {
	t.Parallel()
	for _, op := range []string{"CreateRef", "UpdateRef"} {
		t.Run(op, func(t *testing.T) {
			t.Parallel()
			fake := newFakeRefs()
			if op == "UpdateRef" {
				if _, err := claim.NewStore(fake, clock(t0()), nil).Claim(context.Background(), request("run-old")); err != nil {
					t.Fatalf("first Claim: %v", err)
				}
			}
			notADir := filepath.Join(t.TempDir(), "claims")
			if err := os.WriteFile(notADir, []byte("a file where the journal directory should be"), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			fake.cancelOn, fake.cancel = op, cancel
			store := claim.NewStore(fake, clock(t0().Add(lease)), &claim.Journal{Dir: notADir})
			if _, err := store.Claim(ctx, request("run-1")); err == nil {
				t.Fatal("Claim succeeded although the journal could not record it")
			}
			if !slices.Contains(fake.callLog(), op) {
				t.Fatalf("calls %q: the %s under test was never made", fake.callLog(), op)
			}
			if sha, present := fake.refSHA(repo, ref); present {
				t.Fatalf("ref still points at %s after the journal failed", sha)
			}
		})
	}
}

// A caller gone before the ref is touched, from the start or right after
// the claim's commit, starts no claim.
func TestStore_claim_callerGoneBeforeTheMutation_refusedNoRefCall(t *testing.T) {
	t.Parallel()
	for name, gone := range map[string]func(*fakeRefs, context.CancelFunc){
		"before the call":  func(_ *fakeRefs, cancel context.CancelFunc) { cancel() },
		"after the commit": func(f *fakeRefs, cancel context.CancelFunc) { f.cancelOn, f.cancel = "CommitFile", cancel },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assertNoMutation(t, gone)
		})
	}
}

func assertNoMutation(t *testing.T, gone func(*fakeRefs, context.CancelFunc)) {
	t.Helper()
	fake := newFakeRefs()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gone(fake, cancel)
	_, err := claim.NewStore(fake, clock(t0()), &claim.Journal{Dir: t.TempDir()}).Claim(ctx, request("run-1"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Claim on a cancelled context = %v, want context.Canceled", err)
	}
	for _, call := range fake.callLog() {
		if call == "CreateRef" || call == "UpdateRef" {
			t.Fatalf("calls %q: a ref mutation was sent for a caller already gone", fake.callLog())
		}
	}
}
