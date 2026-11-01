package claim_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/claim"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

const (
	repo  = "fuseone-io/fuseone-forja"
	issue = 12
	ref   = "refs/forja/claims/12"
	lease = 45 * time.Minute
)

func t0() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) }

func clock(at time.Time) func() time.Time { return func() time.Time { return at } }

func request(runID string) domain.ClaimRequest {
	return domain.ClaimRequest{Repository: repo, Issue: issue, RunID: runID, Lease: lease}
}

// claimFile is the wire form of claim.json as a reader outside this
// package would decode it.
type claimFile struct {
	Version    int             `json:"version"`
	RunID      string          `json:"run_id"`
	Repository string          `json:"repository"`
	Issue      int             `json:"issue"`
	CreatedAt  time.Time       `json:"created_at"`
	ExpiresAt  time.Time       `json:"expires_at"`
	Supersedes *supersedesWire `json:"supersedes"`
}

type supersedesWire struct {
	RunID     string    `json:"run_id"`
	SHA       string    `json:"sha"`
	ExpiresAt time.Time `json:"expires_at"`
}

func decodeClaimFile(t *testing.T, fake *fakeRefs, sha string) claimFile {
	t.Helper()
	raw := fake.fileAt(sha, "claim.json")
	if raw == nil {
		t.Fatalf("commit %s holds no claim.json", sha)
	}
	var file claimFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("claim.json at %s: %v: %s", sha, err, raw)
	}
	return file
}

// assertClaimFile compares the decoded claim.json with the expected one,
// following time values instead of comparing their representation.
func assertClaimFile(t *testing.T, got, want claimFile) {
	t.Helper()
	if got.Version != want.Version || got.RunID != want.RunID || got.Repository != want.Repository || got.Issue != want.Issue {
		t.Fatalf("claim.json = %+v, want %+v", got, want)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) || !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Fatalf("claim.json window = %v..%v, want %v..%v", got.CreatedAt, got.ExpiresAt, want.CreatedAt, want.ExpiresAt)
	}
	assertSupersedes(t, got.Supersedes, want.Supersedes)
}

func assertSupersedes(t *testing.T, got, want *supersedesWire) {
	t.Helper()
	if (got == nil) != (want == nil) {
		t.Fatalf("supersedes = %+v, want %+v", got, want)
	}
	if got == nil {
		return
	}
	if got.RunID != want.RunID || got.SHA != want.SHA || !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Fatalf("supersedes = %+v, want %+v", *got, *want)
	}
}

// assertClaim checks the returned claim's coordinates and lease window.
func assertClaim(t *testing.T, got domain.Claim, runID string, created time.Time) {
	t.Helper()
	if got.RunID != runID || got.Repository != repo || got.Issue != issue {
		t.Fatalf("Claim = %+v, want run %s on issue %d of %s", got, runID, issue, repo)
	}
	if !got.CreatedAt.Equal(created) || !got.ExpiresAt.Equal(created.Add(lease)) {
		t.Fatalf("Claim window = %v..%v, want %v..%v", got.CreatedAt, got.ExpiresAt, created, created.Add(lease))
	}
}

func assertJournalHolds(t *testing.T, journal *claim.Journal, want claim.Entry) {
	t.Helper()
	if entries := mustList(t, journal); len(entries) != 1 || entries[0] != want {
		t.Fatalf("journal = %+v, want [%+v]", entries, want)
	}
}

func mustList(t *testing.T, journal *claim.Journal) []claim.Entry {
	t.Helper()
	entries, err := journal.List()
	if err != nil {
		t.Fatalf("journal.List: %v", err)
	}
	return entries
}

func TestStore_claim_free_refPointsAtClaimAndJournalHoldsIt(t *testing.T) {
	t.Parallel()
	fake := newFakeRefs()
	journal := &claim.Journal{Dir: t.TempDir()}
	store := claim.NewStore(fake, clock(t0()), journal)

	got, err := store.Claim(context.Background(), request("run-1"))
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	sha, ok := fake.refSHA(repo, ref)
	if !ok || sha != got.SHA || got.SHA == "" {
		t.Fatalf("ref = %q (%v), Claim.SHA = %q; want the ref at the claim commit", sha, ok, got.SHA)
	}
	if fake.parentOf(sha) != "" {
		t.Fatalf("a fresh claim commit has parent %s, want none", fake.parentOf(sha))
	}
	want := claimFile{Version: 1, RunID: "run-1", Repository: repo, Issue: issue, CreatedAt: t0(), ExpiresAt: t0().Add(lease)}
	assertClaimFile(t, decodeClaimFile(t, fake, sha), want)
	if !strings.Contains(string(fake.fileAt(sha, "claim.json")), `"expires_at":"2026-11-01T12:45:00Z"`) {
		t.Fatalf("expires_at is not RFC3339 UTC: %s", fake.fileAt(sha, "claim.json"))
	}
	assertClaim(t, got, "run-1", t0())
	if got.Supersedes != nil {
		t.Fatalf("Claim.Supersedes = %+v for a free issue, want nil", got.Supersedes)
	}
	assertJournalHolds(t, journal, claim.Entry{Repository: repo, Issue: issue, RunID: "run-1", SHA: sha})
}

func TestStore_claim_heldUnexpired_refusedWithHolderRunID(t *testing.T) {
	t.Parallel()
	fake := newFakeRefs()
	first := claim.NewStore(fake, clock(t0()), nil)
	holder, err := first.Claim(context.Background(), request("run-holder"))
	if err != nil {
		t.Fatalf("first Claim: %v", err)
	}
	// One second before the lease ends the claim is still held.
	second := claim.NewStore(fake, clock(t0().Add(lease-time.Second)), nil)

	_, err = second.Claim(context.Background(), request("run-late"))
	if !errors.Is(err, domain.ErrClaimHeld) {
		t.Fatalf("second Claim = %v, want ErrClaimHeld", err)
	}
	var held *domain.ClaimHeldError
	if !errors.As(err, &held) || held.Holder.RunID != "run-holder" || held.Holder.SHA != holder.SHA {
		t.Fatalf("error %v does not carry the holder %+v", err, holder)
	}
	if !strings.Contains(err.Error(), "run-holder") || strings.Contains(err.Error(), "run-late") {
		t.Fatalf("error %q must name the holder and not the loser", err)
	}
	if sha, _ := fake.refSHA(repo, ref); sha != holder.SHA {
		t.Fatalf("a refused claim moved the ref to %s", sha)
	}
}

func TestStore_claim_concurrent_exactlyOneWinsOthersNameWinner(t *testing.T) {
	t.Parallel()
	const runs = 16
	fake := newFakeRefs()
	store := claim.NewStore(fake, clock(t0()), &claim.Journal{Dir: t.TempDir()})

	type outcome struct {
		runID string
		claim domain.Claim
		err   error
	}
	results := make(chan outcome, runs)
	var wg sync.WaitGroup
	for i := range runs {
		wg.Go(func() {
			id := fmt.Sprintf("run-%02d", i)
			c, err := store.Claim(context.Background(), request(id))
			results <- outcome{runID: id, claim: c, err: err}
		})
	}
	wg.Wait()
	close(results)

	var winners []outcome
	var losers []outcome
	for r := range results {
		if r.err == nil {
			winners = append(winners, r)
		} else {
			losers = append(losers, r)
		}
	}
	if len(winners) != 1 {
		t.Fatalf("%d claims succeeded, want exactly one: %+v", len(winners), winners)
	}
	winner := winners[0]
	if sha, _ := fake.refSHA(repo, ref); sha != winner.claim.SHA {
		t.Fatalf("ref points at %s, the winner's claim is %s", sha, winner.claim.SHA)
	}
	for _, l := range losers {
		if !errors.Is(l.err, domain.ErrClaimHeld) {
			t.Errorf("%s: error %v is not ErrClaimHeld", l.runID, l.err)
		}
		if !strings.Contains(l.err.Error(), winner.runID) {
			t.Errorf("%s: error %q does not name the winner %s", l.runID, l.err, winner.runID)
		}
	}
}

func TestStore_claim_expired_takenOverSupersedesPrevious(t *testing.T) {
	t.Parallel()
	fake := newFakeRefs()
	journal := &claim.Journal{Dir: t.TempDir()}
	old, err := claim.NewStore(fake, clock(t0()), journal).Claim(context.Background(), request("run-old"))
	if err != nil {
		t.Fatalf("first Claim: %v", err)
	}
	later := t0().Add(lease)
	store := claim.NewStore(fake, clock(later), journal)

	got, err := store.Claim(context.Background(), request("run-new"))
	if err != nil {
		t.Fatalf("Claim over an expired lease: %v", err)
	}
	sha, _ := fake.refSHA(repo, ref)
	if sha != got.SHA || sha == old.SHA {
		t.Fatalf("ref = %s, Claim.SHA = %s, old = %s; want the ref moved to the new claim", sha, got.SHA, old.SHA)
	}
	if fake.parentOf(sha) != old.SHA {
		t.Fatalf("new claim's parent = %s, want the superseded claim %s", fake.parentOf(sha), old.SHA)
	}
	want := claimFile{
		Version: 1, RunID: "run-new", Repository: repo, Issue: issue, CreatedAt: later, ExpiresAt: later.Add(lease),
		Supersedes: &supersedesWire{RunID: "run-old", SHA: old.SHA, ExpiresAt: old.ExpiresAt},
	}
	assertClaimFile(t, decodeClaimFile(t, fake, sha), want)
	assertClaim(t, got, "run-new", later)
	if got.Supersedes == nil || got.Supersedes.RunID != "run-old" || got.Supersedes.SHA != old.SHA {
		t.Fatalf("Claim.Supersedes = %+v, want run-old at %s", got.Supersedes, old.SHA)
	}
	assertJournalHolds(t, journal, claim.Entry{Repository: repo, Issue: issue, RunID: "run-new", SHA: sha})
}

// Two runs see the same expired lease. The first update wins; the second
// is not a fast-forward of the ref any more and is refused with the new
// holder's run id, not the expired one's.
func TestStore_claim_expiredRacedByTwo_fastForwardRejectsLoser(t *testing.T) {
	t.Parallel()
	fake := newFakeRefs()
	if _, err := claim.NewStore(fake, clock(t0()), nil).Claim(context.Background(), request("run-old")); err != nil {
		t.Fatalf("first Claim: %v", err)
	}
	later := t0().Add(2 * lease)
	winner := claim.NewStore(fake, clock(later), nil)
	loser := claim.NewStore(fake, clock(later), nil)
	var winnerClaim domain.Claim
	fake.beforeUpdate = func() {
		c, err := winner.Claim(context.Background(), request("run-winner"))
		if err != nil {
			t.Errorf("winner Claim: %v", err)
		}
		winnerClaim = c
	}

	_, err := loser.Claim(context.Background(), request("run-loser"))
	if !errors.Is(err, domain.ErrClaimHeld) {
		t.Fatalf("loser Claim = %v, want ErrClaimHeld", err)
	}
	var held *domain.ClaimHeldError
	if !errors.As(err, &held) || held.Holder.RunID != "run-winner" {
		t.Fatalf("loser's error %v must name run-winner", err)
	}
	if strings.Contains(err.Error(), "run-old") {
		t.Fatalf("loser's error %q names the expired run instead of the new holder", err)
	}
	if sha, _ := fake.refSHA(repo, ref); sha != winnerClaim.SHA {
		t.Fatalf("ref = %s, want the winner's claim %s", sha, winnerClaim.SHA)
	}
}

func TestStore_claim_unreadableClaim_refusedNotTakenOver(t *testing.T) {
	t.Parallel()
	expiredJSON := func(mutate func(m map[string]any)) []byte {
		m := map[string]any{
			"version": 1, "run_id": "run-old", "repository": repo, "issue": issue,
			"created_at": "2026-11-01T10:00:00Z", "expires_at": "2026-11-01T10:45:00Z",
		}
		mutate(m)
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	tests := []struct {
		name  string
		files map[string][]byte
	}{
		{name: "not json", files: map[string][]byte{"claim.json": []byte("not json")}},
		{name: "no claim.json", files: map[string][]byte{"other.json": []byte("{}")}},
		{name: "no run id", files: map[string][]byte{"claim.json": expiredJSON(func(m map[string]any) { delete(m, "run_id") })}},
		{name: "no expiry", files: map[string][]byte{"claim.json": expiredJSON(func(m map[string]any) { delete(m, "expires_at") })}},
		{name: "unknown version", files: map[string][]byte{"claim.json": expiredJSON(func(m map[string]any) { m["version"] = 2 })}},
		{name: "another issue", files: map[string][]byte{"claim.json": expiredJSON(func(m map[string]any) { m["issue"] = 99 })}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := newFakeRefs()
			seeded := fake.seed(repo, ref, tt.files)
			store := claim.NewStore(fake, clock(t0()), nil)

			_, err := store.Claim(context.Background(), request("run-new"))
			assertRefusedUntouched(t, fake, seeded, err, tt.name != "no claim.json")
		})
	}
}

// assertRefusedUntouched checks a claim was refused without being reported
// as held and without the ref being moved or deleted.
func assertRefusedUntouched(t *testing.T, fake *fakeRefs, seeded string, err error, wantUnreadable bool) {
	t.Helper()
	if err == nil {
		t.Fatal("Claim took over an unreadable claim")
	}
	if errors.Is(err, domain.ErrClaimHeld) {
		t.Fatalf("an unreadable claim was reported as held: %v", err)
	}
	if wantUnreadable && !errors.Is(err, domain.ErrClaimUnreadable) {
		t.Fatalf("Claim = %v, want ErrClaimUnreadable", err)
	}
	if sha, _ := fake.refSHA(repo, ref); sha != seeded {
		t.Fatalf("the ref moved to %s; an unreadable claim is never taken over", sha)
	}
	for _, call := range fake.callLog() {
		if call == "UpdateRef" || call == "DeleteRef" {
			t.Fatalf("store called %s on an unreadable claim: %q", call, fake.callLog())
		}
	}
}

func TestStore_claim_invalidRequest_rejectedBeforeAnyCall(t *testing.T) {
	t.Parallel()
	fake := newFakeRefs()
	store := claim.NewStore(fake, clock(t0()), nil)
	bad := request("run-1")
	bad.Issue = 0

	if _, err := store.Claim(context.Background(), bad); err == nil {
		t.Fatal("Claim accepted issue 0")
	}
	if calls := fake.callLog(); len(calls) != 0 {
		t.Fatalf("an invalid request reached the repository: %q", calls)
	}
}

func TestStore_release_removesRefAndJournalEntry(t *testing.T) {
	t.Parallel()
	fake := newFakeRefs()
	journal := &claim.Journal{Dir: t.TempDir()}
	store := claim.NewStore(fake, clock(t0()), journal)
	c, err := store.Claim(context.Background(), request("run-1"))
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}

	if err := store.Release(context.Background(), c); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if sha, present := fake.refSHA(repo, ref); present {
		t.Fatalf("ref still points at %s after Release", sha)
	}
	if entries := mustList(t, journal); len(entries) != 0 {
		t.Fatalf("journal still holds %+v after Release", entries)
	}
}

func TestStore_release_refTakenOver_errClaimLostRefKept(t *testing.T) {
	t.Parallel()
	fake := newFakeRefs()
	journal := &claim.Journal{Dir: t.TempDir()}
	old, err := claim.NewStore(fake, clock(t0()), journal).Claim(context.Background(), request("run-old"))
	if err != nil {
		t.Fatalf("first Claim: %v", err)
	}
	taker := claim.NewStore(fake, clock(t0().Add(2*lease)), &claim.Journal{Dir: t.TempDir()})
	current, err := taker.Claim(context.Background(), request("run-new"))
	if err != nil {
		t.Fatalf("take over: %v", err)
	}

	err = claim.NewStore(fake, clock(t0().Add(2*lease)), journal).Release(context.Background(), old)
	if !errors.Is(err, domain.ErrClaimLost) {
		t.Fatalf("Release of a superseded claim = %v, want ErrClaimLost", err)
	}
	if sha, _ := fake.refSHA(repo, ref); sha != current.SHA {
		t.Fatalf("Release moved or removed the new holder's ref: %s, want %s", sha, current.SHA)
	}
	if entries := mustList(t, journal); len(entries) != 0 {
		t.Fatalf("a lost claim stays in the journal: %+v", entries)
	}
}

func TestStore_release_refGone_errClaimLost(t *testing.T) {
	t.Parallel()
	fake := newFakeRefs()
	store := claim.NewStore(fake, clock(t0()), nil)
	c, err := store.Claim(context.Background(), request("run-1"))
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := fake.DeleteRef(context.Background(), repo, ref); err != nil {
		t.Fatal(err)
	}
	if err := store.Release(context.Background(), c); !errors.Is(err, domain.ErrClaimLost) {
		t.Fatalf("Release of a vanished ref = %v, want ErrClaimLost", err)
	}
}

// A release that could not even read the ref has not lost the claim: the
// journal keeps it so recovery can finish the job.
func TestStore_release_repositoryUnreachable_journalEntryKept(t *testing.T) {
	t.Parallel()
	fake := newFakeRefs()
	journal := &claim.Journal{Dir: t.TempDir()}
	store := claim.NewStore(fake, clock(t0()), journal)
	c, err := store.Claim(context.Background(), request("run-1"))
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	fake.fail["GetRef"] = errors.New("connection reset")

	err = store.Release(context.Background(), c)
	if err == nil || errors.Is(err, domain.ErrClaimLost) {
		t.Fatalf("Release = %v, want an error that is not ErrClaimLost", err)
	}
	if entries := mustList(t, journal); len(entries) != 1 {
		t.Fatalf("journal = %+v, want the entry kept for recovery", entries)
	}
	if _, present := fake.refSHA(repo, ref); !present {
		t.Fatal("the ref was deleted although it could not be verified")
	}
}

func TestStore_recover_journaledRefStillOurs_deleted(t *testing.T) {
	t.Parallel()
	fake := newFakeRefs()
	journal := &claim.Journal{Dir: t.TempDir()}
	c, err := claim.NewStore(fake, clock(t0()), journal).Claim(context.Background(), request("run-crashed"))
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	// A new process starts with the same journal directory.
	restarted := claim.NewStore(fake, clock(t0().Add(time.Minute)), journal)

	released, err := restarted.Recover(context.Background())
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if len(released) != 1 || released[0].RunID != "run-crashed" || released[0].Issue != issue || released[0].Repository != repo || released[0].SHA != c.SHA {
		t.Fatalf("Recover released %+v, want the crashed run's claim", released)
	}
	if sha, present := fake.refSHA(repo, ref); present {
		t.Fatalf("ref still points at %s after recovery", sha)
	}
	if entries := mustList(t, journal); len(entries) != 0 {
		t.Fatalf("journal = %+v after recovery, want empty", entries)
	}
}

func TestStore_recover_journaledRefTakenOver_leftAlone(t *testing.T) {
	t.Parallel()
	fake := newFakeRefs()
	journal := &claim.Journal{Dir: t.TempDir()}
	if _, err := claim.NewStore(fake, clock(t0()), journal).Claim(context.Background(), request("run-crashed")); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	taker := claim.NewStore(fake, clock(t0().Add(2*lease)), &claim.Journal{Dir: t.TempDir()})
	current, err := taker.Claim(context.Background(), request("run-new"))
	if err != nil {
		t.Fatalf("take over: %v", err)
	}

	released, err := claim.NewStore(fake, clock(t0().Add(2*lease)), journal).Recover(context.Background())
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if len(released) != 0 {
		t.Fatalf("Recover released %+v, want nothing: the ref is another run's", released)
	}
	if sha, _ := fake.refSHA(repo, ref); sha != current.SHA {
		t.Fatalf("recovery touched the new holder's ref: %s, want %s", sha, current.SHA)
	}
	if entries := mustList(t, journal); len(entries) != 0 {
		t.Fatalf("journal = %+v after recovery, want the stale entry gone", entries)
	}
}

func TestStore_recover_journaledRefGone_entryRemoved(t *testing.T) {
	t.Parallel()
	fake := newFakeRefs()
	journal := &claim.Journal{Dir: t.TempDir()}
	if err := journal.Put(claim.Entry{Repository: repo, Issue: issue, RunID: "run-crashed", SHA: "deadbeef"}); err != nil {
		t.Fatal(err)
	}

	released, err := claim.NewStore(fake, clock(t0()), journal).Recover(context.Background())
	if err != nil || len(released) != 0 {
		t.Fatalf("Recover = %+v, %v; want nothing released and no error", released, err)
	}
	if entries := mustList(t, journal); len(entries) != 0 {
		t.Fatalf("journal = %+v, want the entry of a vanished ref removed", entries)
	}
}

func TestStore_recover_noJournal_noop(t *testing.T) {
	t.Parallel()
	fake := newFakeRefs()
	fake.seed(repo, ref, map[string][]byte{"claim.json": []byte("{}")})

	released, err := claim.NewStore(fake, clock(t0()), nil).Recover(context.Background())
	if err != nil || len(released) != 0 {
		t.Fatalf("Recover without a journal = %+v, %v; want nothing and no error", released, err)
	}
	if calls := fake.callLog(); len(calls) != 0 {
		t.Fatalf("Recover without a journal reached the repository: %q", calls)
	}
}

func TestStore_recover_repositoryUnreachable_failsAndKeepsEntry(t *testing.T) {
	t.Parallel()
	fake := newFakeRefs()
	journal := &claim.Journal{Dir: t.TempDir()}
	if _, err := claim.NewStore(fake, clock(t0()), journal).Claim(context.Background(), request("run-crashed")); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	fake.fail["GetRef"] = errors.New("connection reset")

	if _, err := claim.NewStore(fake, clock(t0()), journal).Recover(context.Background()); err == nil {
		t.Fatal("Recover succeeded although the ref could not be read")
	}
	if entries := mustList(t, journal); len(entries) != 1 {
		t.Fatalf("journal = %+v, want the entry kept", entries)
	}
}

func TestStore_zeroValue_failsLoudly(t *testing.T) {
	t.Parallel()
	var store claim.Store
	if _, err := store.Claim(context.Background(), request("run-1")); err == nil {
		t.Fatal("a Store with no Refs and no clock claimed")
	}
}
