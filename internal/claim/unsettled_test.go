package claim_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/claim"
)

// spentBudget is a mutation budget a stalled answer uses up at once; the
// follow-up budget stays long, so only the mutation's runs out.
const (
	spentBudget  = 20 * time.Millisecond
	followBudget = time.Minute
)

// mutations names the two ref mutations a claim makes, with the setup that
// leads the store to each: a free issue is created, an expired claim moved.
var mutations = map[string]func(t *testing.T, fake *fakeRefs){
	"CreateRef": func(*testing.T, *fakeRefs) {},
	"UpdateRef": func(t *testing.T, fake *fakeRefs) {
		t.Helper()
		if _, err := claim.NewStore(fake, clock(t0()), nil).Claim(context.Background(), request("run-old")); err != nil {
			t.Fatalf("first Claim: %v", err)
		}
	},
}

// newStalling builds a store whose mutation budget runs out while GitHub
// has applied the mutation and its answer is still on the way.
func newStalling(t *testing.T, op string, journal *claim.Journal) (*fakeRefs, *claim.Store) {
	t.Helper()
	fake := newFakeRefs()
	mutations[op](t, fake)
	fake.stallOn = op
	store := claim.WithTimeouts(claim.NewStore(fake, clock(t0().Add(lease)), journal), spentBudget, followBudget)
	return fake, store
}

// assertJournaledIfPresent fails when the ref exists and the journal does
// not name its commit: nothing would release it after a crash.
func assertJournaledIfPresent(t *testing.T, fake *fakeRefs, journal *claim.Journal) {
	t.Helper()
	sha, present := fake.refSHA(repo, ref)
	if !present {
		return
	}
	for _, e := range mustList(t, journal) {
		if e.Issue == issue && e.SHA == sha {
			return
		}
	}
	t.Fatalf("ref points at %s and the journal holds %+v: the claim is orphaned", sha, mustList(t, journal))
}

// GitHub applies the creation or the move and the answer is lost when the
// mutation's budget runs out: the store reads the ref back, finds its own
// commit there, and holds the claim journaled, so the run releases it.
func TestStore_claim_mutationAppliedAnswerLost_heldJournaledAndReleased(t *testing.T) {
	t.Parallel()
	for op := range mutations {
		t.Run(op, func(t *testing.T) {
			t.Parallel()
			journal := &claim.Journal{Dir: t.TempDir()}
			fake, store := newStalling(t, op, journal)
			c, err := store.Claim(context.Background(), request("run-1"))
			assertJournaledIfPresent(t, fake, journal)
			if err != nil {
				t.Fatalf("Claim = %v, want the claim GitHub applied held", err)
			}
			settle(t, store, c, err)
			assertNothingLeft(t, fake, journal)
		})
	}
}

// A mutation that fails before GitHub applies it is refused as before: the
// ref read back is not the claim's, and nothing is journaled.
func TestStore_claim_mutationFailedUnapplied_refusedNothingJournaled(t *testing.T) {
	t.Parallel()
	for op := range mutations {
		t.Run(op, func(t *testing.T) {
			t.Parallel()
			fake := newFakeRefs()
			mutations[op](t, fake)
			before, _ := fake.refSHA(repo, ref)
			fake.fail[op] = errors.New("fake: connection reset")
			journal := &claim.Journal{Dir: t.TempDir()}
			store := claim.NewStore(fake, clock(t0().Add(lease)), journal)
			if _, err := store.Claim(context.Background(), request("run-1")); err == nil {
				t.Fatal("Claim succeeded although the mutation was never applied")
			}
			if after, _ := fake.refSHA(repo, ref); after != before {
				t.Fatalf("ref moved from %q to %q", before, after)
			}
			if entries := mustList(t, journal); len(entries) != 0 {
				t.Fatalf("journal holds %+v for a claim never made", entries)
			}
		})
	}
}

// When the answer is lost and the ref cannot be read back either, the
// claim's commit is journaled anyway: recovery then releases the ref if
// the mutation did apply.
func TestStore_claim_answerLostAndRefUnreadable_journaledForRecovery(t *testing.T) {
	t.Parallel()
	journal := &claim.Journal{Dir: t.TempDir()}
	fake, store := newStalling(t, "CreateRef", journal)
	fake.fail["GetRef"] = errors.New("fake: connection reset")
	if _, err := store.Claim(context.Background(), request("run-1")); err == nil {
		t.Fatal("Claim succeeded although its outcome could not be verified")
	}
	assertJournaledIfPresent(t, fake, journal)
	delete(fake.fail, "GetRef")
	if _, err := store.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	assertNothingLeft(t, fake, journal)
}

// The longest a claim keeps its caller's run waiting once the caller left
// is the mutation, then the read-back of an answer it lost: the two
// budgets claim.SettleBound adds up. A further call there outgrows it, and
// the drain sized from it, so it fails here first.
func TestStore_claim_callerGoneAndAnswerLost_settlesInTheBudgetsCounted(t *testing.T) {
	t.Parallel()
	for op := range mutations {
		t.Run(op, func(t *testing.T) {
			t.Parallel()
			journal := &claim.Journal{Dir: t.TempDir()}
			fake, store := newStalling(t, op, journal)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			fake.cancelOn, fake.cancel = op, cancel
			c, err := store.Claim(ctx, request("run-1"))
			if err != nil {
				t.Fatalf("Claim = %v, want the claim GitHub applied held", err)
			}
			calls := fake.callLog()
			after := calls[slices.Index(calls, op):]
			if want := []string{op, "GetRef"}; !slices.Equal(after, want) {
				t.Fatalf("calls from the mutation on = %q, want %q: one budget each", after, want)
			}
			settle(t, store, c, err)
		})
	}
}
