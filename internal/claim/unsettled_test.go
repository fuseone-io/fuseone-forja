package claim_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/claim"
	"github.com/fuseone-io/fuseone-forja/internal/github"
)

// spentBudget is a mutation budget a stalled answer uses up at once; the
// follow-up budget stays long, so only the mutation's runs out.
const (
	spentBudget  = 20 * time.Millisecond
	followBudget = time.Minute
)

// mutations names the two ref mutations a claim makes, with the setup that
// leads the store to each: a free issue is created, an expired claim moved.
func mutations() map[string]func(t *testing.T, fake *fakeRefs) {
	return map[string]func(t *testing.T, fake *fakeRefs){
		"CreateRef": func(*testing.T, *fakeRefs) {},
		"UpdateRef": func(t *testing.T, fake *fakeRefs) {
			t.Helper()
			if _, err := claim.NewStore(fake, clock(t0()), nil).Claim(context.Background(), request("run-old")); err != nil {
				t.Fatalf("first Claim: %v", err)
			}
		},
	}
}

// A request on an issue another process holds fails on the way: the ref
// reads back at the holder's commit, which the create can never move, so
// nothing is journaled for it.
func TestStore_claim_outcomeOpenOnAnIssueHeldElsewhere_nothingJournaled(t *testing.T) {
	t.Parallel()
	fake := newFakeRefs()
	if _, err := claim.NewStore(fake, clock(t0()), nil).Claim(context.Background(), request("run-elsewhere")); err != nil {
		t.Fatalf("first Claim: %v", err)
	}
	fake.fail["CreateRef"] = errors.New("fake: connection reset")
	journal := &claim.Journal{Dir: t.TempDir()}
	if _, err := claim.NewStore(fake, clock(t0().Add(time.Minute)), journal).Claim(context.Background(), request("run-2")); err == nil {
		t.Fatal("second Claim succeeded on a held issue")
	}
	if entries := mustList(t, journal); len(entries) != 0 {
		t.Fatalf("journal holds %+v for a create that can never apply", entries)
	}
}

// A take-over fails on the way while the expired claim's ref is deleted:
// a move of a ref that is gone can never apply, so nothing is journaled.
func TestStore_claim_takeOverOpenOnAVanishedRef_nothingJournaled(t *testing.T) {
	t.Parallel()
	fake := newFakeRefs()
	mutations()["UpdateRef"](t, fake)
	fake.beforeUpdate = func() {
		if err := fake.DeleteRef(context.Background(), repo, ref); err != nil {
			t.Errorf("delete the expired claim's ref: %v", err)
		}
	}
	fake.fail["UpdateRef"] = errors.New("fake: connection reset")
	journal := &claim.Journal{Dir: t.TempDir()}
	if _, err := claim.NewStore(fake, clock(t0().Add(lease)), journal).Claim(context.Background(), request("run-1")); err == nil {
		t.Fatal("Claim succeeded although its move failed")
	}
	if entries := mustList(t, journal); len(entries) != 0 {
		t.Fatalf("journal holds %+v for a move of a ref that is gone", entries)
	}
}

// A request on an issue this process holds fails on the way: GitHub would
// have refused it, so the holder's ref and journal entry stay as they
// were, whether the ref reads back as the holder's or cannot be read.
func TestStore_claim_outcomeOpenOnAHeldIssue_holdersEntryKept(t *testing.T) {
	t.Parallel()
	for name, unreadable := range map[string]bool{"holder read back": false, "read-back fails": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fake := newFakeRefs()
			journal := &claim.Journal{Dir: t.TempDir()}
			held, err := claim.NewStore(fake, clock(t0()), journal).Claim(context.Background(), request("run-held"))
			if err != nil {
				t.Fatalf("first Claim: %v", err)
			}
			fake.fail["CreateRef"] = errors.New("fake: connection reset")
			if unreadable {
				fake.fail["GetRef"] = errors.New("fake: connection reset")
			}
			if _, err := claim.NewStore(fake, clock(t0().Add(time.Minute)), journal).Claim(context.Background(), request("run-2")); err == nil {
				t.Fatal("second Claim succeeded on a held issue")
			}
			want := []claim.Entry{{Repository: repo, Issue: issue, RunID: "run-held", SHA: held.SHA}}
			if got := mustList(t, journal); !slices.Equal(got, want) {
				t.Fatalf("journal holds %+v, want the holder's %+v", got, want)
			}
		})
	}
}

// newStalling builds a store whose mutation budget runs out while GitHub
// has applied the mutation and its answer is still on the way.
func newStalling(t *testing.T, op string, journal *claim.Journal) (*fakeRefs, *claim.Store) {
	t.Helper()
	fake := newFakeRefs()
	mutations()[op](t, fake)
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
	for op := range mutations() {
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

// refusedMutation is a mutation GitHub answered with a 4xx: it was not
// applied, and nothing is left to settle.
func refusedMutation() error {
	return &github.StatusError{Status: 403, Message: "Resource not accessible by integration"}
}

// A mutation GitHub refused is the answer: the ref is not read back, it is
// left as it was, and nothing is journaled.
func TestStore_claim_mutationRefused_refusedNothingReadBackOrJournaled(t *testing.T) {
	t.Parallel()
	for op := range mutations() {
		t.Run(op, func(t *testing.T) {
			t.Parallel()
			fake := newFakeRefs()
			mutations()[op](t, fake)
			before, _ := fake.refSHA(repo, ref)
			fake.fail[op] = refusedMutation()
			journal := &claim.Journal{Dir: t.TempDir()}
			store := claim.NewStore(fake, clock(t0().Add(lease)), journal)
			if _, err := store.Claim(context.Background(), request("run-1")); err == nil {
				t.Fatal("Claim succeeded although GitHub refused the mutation")
			}
			calls := fake.callLog()
			if after := calls[slices.Index(calls, op)+1:]; len(after) != 0 {
				t.Fatalf("calls after the refused %s = %q, want none", op, after)
			}
			if after, _ := fake.refSHA(repo, ref); after != before {
				t.Fatalf("ref moved from %q to %q", before, after)
			}
			if entries := mustList(t, journal); len(entries) != 0 {
				t.Fatalf("journal holds %+v for a claim GitHub refused", entries)
			}
		})
	}
}

// A mutation whose request failed on the way leaves its outcome open: the
// ref read back is not the claim's yet, but GitHub may still apply it. The
// claim's commit is journaled, so recovery releases the ref if GitHub did
// apply it late, and leaves the ref as it was if not.
func TestStore_claim_mutationOutcomeOpen_journaledRecoveryReleasesALateApply(t *testing.T) {
	t.Parallel()
	for op := range mutations() {
		for name, tt := range map[string]struct {
			cause error
			late  bool
		}{
			"reset, applied late":  {errors.New("fake: connection reset"), true},
			"reset, never applied": {errors.New("fake: connection reset"), false},
			"502, applied late":    {&github.StatusError{Status: 502, Message: "Server Error"}, true},
		} {
			late := tt.late
			t.Run(op+"/"+name, func(t *testing.T) {
				t.Parallel()
				fake := newFakeRefs()
				mutations()[op](t, fake)
				before, _ := fake.refSHA(repo, ref)
				fake.fail[op] = tt.cause
				journal := &claim.Journal{Dir: t.TempDir()}
				store := claim.NewStore(fake, clock(t0().Add(lease)), journal)
				if _, err := store.Claim(context.Background(), request("run-1")); err == nil {
					t.Fatal("Claim succeeded although its outcome was open")
				}
				entries := mustList(t, journal)
				if len(entries) != 1 || entries[0].RunID != "run-1" {
					t.Fatalf("journal holds %+v, want the open claim of run-1", entries)
				}
				want := before
				if late {
					fake.point(repo, ref, entries[0].SHA)
					want = ""
				}
				if _, err := store.Recover(context.Background()); err != nil {
					t.Fatalf("Recover: %v", err)
				}
				if after, _ := fake.refSHA(repo, ref); after != want {
					t.Fatalf("after recovery the ref points at %q, want %q", after, want)
				}
				if entries := mustList(t, journal); len(entries) != 0 {
					t.Fatalf("journal still holds %+v", entries)
				}
			})
		}
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
	for op := range mutations() {
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
			mutation, followUp := claim.Budgets()
			if want := mutation + followUp; claim.SettleBound != want {
				t.Fatalf("SettleBound = %v, want %v: the mutation's budget and one follow-up's", claim.SettleBound, want)
			}
			settle(t, store, c, err)
		})
	}
}
