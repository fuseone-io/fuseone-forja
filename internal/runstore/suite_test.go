package runstore_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// store is what forja.implement writes and forja.status reads, as every
// run store implements it.
type store interface {
	Put(ctx context.Context, run domain.Run) error
	Get(ctx context.Context, id string) (domain.Run, error)
	GetOwned(ctx context.Context, client, id string) (domain.Run, error)
	List(ctx context.Context, f domain.RunFilter) ([]domain.Run, error)
}

// opener returns an empty store of its own to each call.
type opener func(t *testing.T) store

// behaviours is the contract every run store passes: what forja.status can
// observe does not depend on where the runs are kept.
var behaviours = []struct {
	name string
	test func(t *testing.T, open opener)
}{
	{"putThenGet_returnsEqualRun", test_putThenGet_returnsEqualRun},
	{"unknownID_errRunNotFound", test_unknownID_errRunNotFound},
	{"putInvalidRun_rejectedAndNotStored", test_putInvalidRun_rejectedAndNotStored},
	{"putExistingID_replacesRun", test_putExistingID_replacesRun},
	{"getReturnsCopy_callerMutationNotVisibleOnNextGet", test_getReturnsCopy_callerMutationNotVisibleOnNextGet},
	{"putStoresCopy_callerMutationAfterPutNotVisible", test_putStoresCopy_callerMutationAfterPutNotVisible},
	{"get_egressListNotShared", test_get_egressListNotShared},
	{"proofLists_notSharedWithCaller", test_proofLists_notSharedWithCaller},
	{"get_qaExcludedListNotShared", test_get_qaExcludedListNotShared},
	{"get_inversionsNotShared", test_get_inversionsNotShared},
	{"tokensAndReason_roundTripAsValues", test_tokensAndReason_roundTripAsValues},
	{"limitsAndDiffLines_notSharedWithCaller", test_limitsAndDiffLines_notSharedWithCaller},
	{"get_registryListNotShared", test_get_registryListNotShared},
	{"stagesAndGateStreams_notSharedWithCaller", test_stagesAndGateStreams_notSharedWithCaller},
	{"list_filtersSortsAndReturnsCopies", test_list_filtersSortsAndReturnsCopies},
	{"list_noMatchEmptyNotNilAndInvalidFilterRejected", test_list_noMatchEmptyNotNilAndInvalidFilterRejected},
	{"concurrentPutAndGet_noRace", test_concurrentPutAndGet_noRace},
	{"everyField_roundTripsExactly", test_everyField_roundTripsExactly},
	{"nulAndNanoseconds_roundTripExactly", test_nulAndNanoseconds_roundTripExactly},
	{"emptyAndNilLists_keptApart", test_emptyAndNilLists_keptApart},
	{"list_windowBoundsToTheNanosecond", test_list_windowBoundsToTheNanosecond},
	{"list_toolAndStateFilters", test_list_toolAndStateFilters},
	{"putExistingIDOtherClient_refusedRunKeepsItsClient", test_putExistingIDOtherClient_refusedRunKeepsItsClient},
	{"listByClient_onlyThatClientsRuns", test_listByClient_onlyThatClientsRuns},
	{"getOwned_anotherClientsRunIsUnknown", test_getOwned_anotherClientsRunIsUnknown},
}

// runSuite runs every behaviour against stores open returns.
func runSuite(t *testing.T, open opener) {
	t.Helper()
	for _, b := range behaviours {
		t.Run(b.name, func(t *testing.T) { b.test(t, open) })
	}
}

func sampleRun(id string) domain.Run {
	return domain.Run{
		ID:         id,
		Tool:       domain.ToolImplement,
		Repository: "fuseone-io/fuseone-forja",
		Issue:      5,
		State:      domain.StateRunning,
		StartedAt:  time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
		Client:     "fuseone-pilot",
	}
}

func test_putThenGet_returnsEqualRun(t *testing.T, open opener) {
	t.Parallel()
	ctx := context.Background()
	store := open(t)

	finished := time.Date(2026, 10, 31, 12, 5, 0, 0, time.UTC)
	want := sampleRun("run-1")
	want.State = domain.StateSucceeded
	want.FinishedAt = &finished

	if err := store.Put(ctx, want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := store.Get(ctx, "run-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	assertSameRun(t, got, want)
}

// assertSameRun compares two runs field by field, following the pointer
// field instead of comparing addresses.
func assertSameRun(t *testing.T, got, want domain.Run) {
	t.Helper()
	scalarsMatch := got.ID == want.ID &&
		got.Tool == want.Tool &&
		got.Repository == want.Repository &&
		got.Client == want.Client &&
		got.Issue == want.Issue &&
		got.State == want.State &&
		got.StartedAt.Equal(want.StartedAt)
	if !scalarsMatch {
		t.Fatalf("run = %+v, want %+v", got, want)
	}
	if !sameFinishedAt(got.FinishedAt, want.FinishedAt) {
		t.Fatalf("FinishedAt = %v, want %v", got.FinishedAt, want.FinishedAt)
	}
}

func sameFinishedAt(got, want *time.Time) bool {
	if got == nil || want == nil {
		return got == want
	}
	return got.Equal(*want)
}

func test_unknownID_errRunNotFound(t *testing.T, open opener) {
	t.Parallel()
	ctx := context.Background()
	store := open(t)
	if err := store.Put(ctx, sampleRun("run-1")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	_, err := store.Get(ctx, "does-not-exist")
	if !errors.Is(err, domain.ErrRunNotFound) {
		t.Fatalf("Get error = %v, want errors.Is ErrRunNotFound", err)
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("Get error %q does not name the id", err)
	}
}

func test_putInvalidRun_rejectedAndNotStored(t *testing.T, open opener) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(r *domain.Run)
	}{
		{name: "empty id", mutate: func(r *domain.Run) { r.ID = "" }},
		{name: "unknown tool", mutate: func(r *domain.Run) { r.Tool = "forja.deploy" }},
		{name: "unknown state", mutate: func(r *domain.Run) { r.State = "paused" }},
		// A text column holds neither; the store in memory must refuse
		// what Postgres cannot keep.
		{name: "id with a NUL", mutate: func(r *domain.Run) { r.ID = "run-\x00" }},
		{name: "id not UTF-8", mutate: func(r *domain.Run) { r.ID = "run-\xff" }},
		{name: "repository with a NUL", mutate: func(r *domain.Run) { r.Repository = "acme/api\x00" }},
		{name: "repository not UTF-8", mutate: func(r *domain.Run) { r.Repository = "acme/\xffapi" }},
		{name: "no client", mutate: func(r *domain.Run) { r.Client = "" }},
		{name: "client with a NUL", mutate: func(r *domain.Run) { r.Client = "pilot\x00" }},
		{name: "client not UTF-8", mutate: func(r *domain.Run) { r.Client = "pilot\xff" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store := open(t)
			invalid := sampleRun("run-invalid")
			tt.mutate(&invalid)

			if err := store.Put(ctx, invalid); err == nil {
				t.Fatal("Put accepted an invalid run")
			}
			if _, err := store.Get(ctx, invalid.ID); !errors.Is(err, domain.ErrRunNotFound) {
				t.Fatalf("Get after rejected Put: error = %v, want ErrRunNotFound", err)
			}
		})
	}
}

func test_putExistingID_replacesRun(t *testing.T, open opener) {
	t.Parallel()
	ctx := context.Background()
	store := open(t)

	first := sampleRun("run-1")
	if err := store.Put(ctx, first); err != nil {
		t.Fatalf("Put first: %v", err)
	}
	finished := first.StartedAt.Add(3 * time.Minute)
	second := first
	second.State = domain.StateFailed
	second.FinishedAt = &finished
	if err := store.Put(ctx, second); err != nil {
		t.Fatalf("Put second: %v", err)
	}

	got, err := store.Get(ctx, "run-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != domain.StateFailed {
		t.Fatalf("State = %q, want %q", got.State, domain.StateFailed)
	}
	if got.FinishedAt == nil || !got.FinishedAt.Equal(finished) {
		t.Fatalf("FinishedAt = %v, want %v", got.FinishedAt, finished)
	}
}

func test_getReturnsCopy_callerMutationNotVisibleOnNextGet(t *testing.T, open opener) {
	t.Parallel()
	ctx := context.Background()
	store := open(t)

	finished := time.Date(2026, 10, 31, 12, 5, 0, 0, time.UTC)
	original := sampleRun("run-1")
	original.State = domain.StateSucceeded
	original.FinishedAt = &finished
	if err := store.Put(ctx, original); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := store.Get(ctx, "run-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got.State = domain.StateFailed
	*got.FinishedAt = finished.Add(time.Hour)

	again, err := store.Get(ctx, "run-1")
	if err != nil {
		t.Fatalf("Get again: %v", err)
	}
	if again.State != domain.StateSucceeded {
		t.Fatalf("caller mutation of State leaked into the store: %q", again.State)
	}
	if again.FinishedAt == nil || !again.FinishedAt.Equal(finished) {
		t.Fatalf("caller mutation of *FinishedAt leaked into the store: %v", again.FinishedAt)
	}
}

func test_putStoresCopy_callerMutationAfterPutNotVisible(t *testing.T, open opener) {
	t.Parallel()
	ctx := context.Background()
	store := open(t)

	want := time.Date(2026, 10, 31, 12, 5, 0, 0, time.UTC)
	finished := want
	original := sampleRun("run-1")
	original.State = domain.StateSucceeded
	original.FinishedAt = &finished
	if err := store.Put(ctx, original); err != nil {
		t.Fatalf("Put: %v", err)
	}
	*original.FinishedAt = want.Add(time.Hour)

	got, err := store.Get(ctx, "run-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.FinishedAt == nil || !got.FinishedAt.Equal(want) {
		t.Fatalf("caller mutation of *FinishedAt after Put leaked into the store: %v", got.FinishedAt)
	}
}

// The denials are evidence: neither the writer after Put nor a reader
// after Get can change what the store holds through a shared list.
func test_get_egressListNotShared(t *testing.T, open opener) {
	t.Parallel()
	ctx := context.Background()
	store := open(t)
	at := time.Date(2026, 10, 31, 12, 1, 0, 0, time.UTC)
	want := domain.EgressDenial{At: at, Kind: domain.EgressConnect, Destination: "example.com:443"}
	original := sampleRun("run-1")
	original.Egress = domain.EgressReport{Denied: []domain.EgressDenial{want}, Count: 3}
	if err := store.Put(ctx, original); err != nil {
		t.Fatalf("Put: %v", err)
	}
	original.Egress.Denied[0].Destination = "changed-after-put:443"

	got, err := store.Get(ctx, "run-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Egress.Denied) != 1 || got.Egress.Denied[0] != want || got.Egress.Count != 3 {
		t.Fatalf("egress after the writer's change = %+v, want [%+v] count 3", got.Egress, want)
	}
	got.Egress.Denied[0].Destination = "changed-after-get:443"
	again, err := store.Get(ctx, "run-1")
	if err != nil {
		t.Fatalf("Get again: %v", err)
	}
	if again.Egress.Denied[0] != want {
		t.Fatalf("egress after a reader's change = %+v, want %+v", again.Egress.Denied[0], want)
	}
}

// The changed paths are evidence: neither list is shared with the writer
// after Put nor with a reader after Get.
func test_proofLists_notSharedWithCaller(t *testing.T, open opener) {
	t.Parallel()
	ctx := context.Background()
	store := open(t)
	proof := domain.FileChange{Path: "Makefile", Kind: domain.ChangeModified, Added: 1}
	human := domain.FileChange{Path: ".github/CODEOWNERS", Kind: domain.ChangeAdded, Added: 2}
	original := sampleRun("run-1")
	original.Proof = domain.Proof{ProofPaths: []domain.FileChange{proof}, HumanMergePaths: []domain.FileChange{human}}
	if err := store.Put(ctx, original); err != nil {
		t.Fatalf("Put: %v", err)
	}
	original.Proof.ProofPaths[0].Path, original.Proof.HumanMergePaths[0].Path = "after-put", "after-put"

	got, err := store.Get(ctx, "run-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Proof.ProofPaths) != 1 || got.Proof.ProofPaths[0] != proof || len(got.Proof.HumanMergePaths) != 1 || got.Proof.HumanMergePaths[0] != human {
		t.Fatalf("proof after the writer's change = %+v, want [%+v] and [%+v]", got.Proof, proof, human)
	}
	got.Proof.ProofPaths[0].Path, got.Proof.HumanMergePaths[0].Path = "after-get", "after-get"
	again, err := store.Get(ctx, "run-1")
	if err != nil {
		t.Fatalf("Get again: %v", err)
	}
	if again.Proof.ProofPaths[0] != proof || again.Proof.HumanMergePaths[0] != human {
		t.Fatalf("proof after a reader's change = %+v", again.Proof)
	}
}

// The excluded paths are what a verify run's verdict was reached on: no
// writer after Put nor reader after Get changes them through a shared list.
func test_get_qaExcludedListNotShared(t *testing.T, open opener) {
	t.Parallel()
	ctx := context.Background()
	store := open(t)
	original := sampleRun("run-1")
	original.Tool = domain.ToolVerify
	original.QA = domain.QA{Verdict: domain.VerdictNotVerifiable, Reason: domain.NotVerifiableOnlyExcludedPaths, Excluded: []string{"README.md"}}
	if err := store.Put(ctx, original); err != nil {
		t.Fatalf("Put: %v", err)
	}
	original.QA.Excluded[0] = "changed-after-put.go"

	got, err := store.Get(ctx, "run-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(got.QA.Excluded, []string{"README.md"}) {
		t.Fatalf("excluded after the writer's change = %q, want [README.md]", got.QA.Excluded)
	}
	got.QA.Excluded[0] = "changed-after-get.go"
	again, err := store.Get(ctx, "run-1")
	if err != nil {
		t.Fatalf("Get again: %v", err)
	}
	if !reflect.DeepEqual(again.QA.Excluded, []string{"README.md"}) {
		t.Fatalf("excluded after a reader's change = %q, want [README.md]", again.QA.Excluded)
	}
}

// The inversions are what a verdict that bites or does not bite was
// reached on: no reader after Get changes their paths, their gates or
// their streams in the store.
func test_get_inversionsNotShared(t *testing.T, open opener) {
	t.Parallel()
	ctx := context.Background()
	store := open(t)
	inversions := func() []domain.Inversion {
		return []domain.Inversion{{
			Branch: "forja-qa/1", Paths: []string{"internal/cost.go"}, Admitted: true, Outcome: domain.InversionBit,
			Gates: domain.GateReport{
				Setup:      []domain.GateResult{{Name: "setup[0]", Stdout: []byte("setup")}},
				Gates:      []domain.GateResult{{Name: "test", Status: domain.StepFailed, ExitCode: 2, Stderr: []byte("FAIL")}},
				FailedStep: "test", Egress: domain.EgressReport{Denied: []domain.EgressDenial{{Destination: "example.com:443"}}},
			},
		}}
	}
	run := sampleRun("run-1")
	run.Tool, run.Inversions = domain.ToolVerify, inversions()
	if err := store.Put(ctx, run); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := store.Get(ctx, "run-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got.Inversions[0].Paths[0] = "changed.go"
	got.Inversions[0].Gates.Gates[0].Stderr[0] = 'X'
	got.Inversions[0].Gates.Setup[0].Stdout[0] = 'X'
	got.Inversions[0].Gates.Egress.Denied[0].Destination = "evil:443"
	got.Inversions[0].Gates.Gates[0].Name = "renamed"
	got.Inversions[0].Outcome = domain.InversionEscaped
	run.Inversions[0].Paths[0] = "changed-after-put.go"

	again, err := store.Get(ctx, "run-1")
	if err != nil {
		t.Fatalf("Get again: %v", err)
	}
	if want := inversions(); !reflect.DeepEqual(again.Inversions, want) {
		t.Fatalf("inversions after a reader's change = %+v, want %+v", again.Inversions, want)
	}
}

// The tokens and the reason are values: what Get returns is what was put,
// and a reader's copy is its own.
func test_tokensAndReason_roundTripAsValues(t *testing.T, open opener) {
	t.Parallel()
	ctx := context.Background()
	store := open(t)
	run := sampleRun("run-1")
	run.Tokens = domain.TokenUsage{Input: 1, CacheCreation: 2, CacheRead: 4, Output: 8}
	run.Reason = domain.ReasonTokenCap
	if err := store.Put(ctx, run); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := store.Get(ctx, "run-1")
	if err != nil || got.Tokens != run.Tokens || got.Reason != run.Reason {
		t.Fatalf("Get = tokens %+v reason %q (%v), want %+v %q", got.Tokens, got.Reason, err, run.Tokens, run.Reason)
	}
	got.Tokens.Output = 999
	if again, _ := store.Get(ctx, "run-1"); again.Tokens != run.Tokens {
		t.Fatalf("tokens after a reader's change = %+v, want %+v", again.Tokens, run.Tokens)
	}
}

// The limits and the measured diff are values too: a reader changing its
// copy, or the writer changing the run after Put, changes neither what the
// store holds.
func test_limitsAndDiffLines_notSharedWithCaller(t *testing.T, open opener) {
	t.Parallel()
	ctx := context.Background()
	store := open(t)
	run := sampleRun("run-1")
	lines := 120
	run.Limits, run.DiffLines = domain.Limits{WallClock: 20 * time.Minute, MaxDiffLines: 400}, &lines
	if err := store.Put(ctx, run); err != nil {
		t.Fatalf("Put: %v", err)
	}
	lines = 999

	got, err := store.Get(ctx, "run-1")
	if err != nil || got.DiffLines == nil || *got.DiffLines != 120 || got.Limits != run.Limits {
		t.Fatalf("Get = diff lines %v limits %+v (%v), want 120 under %+v", got.DiffLines, got.Limits, err, run.Limits)
	}
	*got.DiffLines = 7
	if again, _ := store.Get(ctx, "run-1"); again.DiffLines == nil || *again.DiffLines != 120 {
		t.Fatalf("diff lines after a reader's change = %v, want 120", again.DiffLines)
	}
}

// The registry accesses are evidence too, under the same rule as the
// denials.
func test_get_registryListNotShared(t *testing.T, open opener) {
	t.Parallel()
	ctx := context.Background()
	store := open(t)
	at := time.Date(2026, 10, 31, 12, 1, 0, 0, time.UTC)
	want := domain.RegistryAccess{At: at, Registry: "ghcr.io/my-org", Path: "/pkg", Outcome: domain.RegistryAllowed, Status: 200}
	original := sampleRun("run-1")
	original.Registry = domain.RegistryReport{Accesses: []domain.RegistryAccess{want}, Count: 3}
	if err := store.Put(ctx, original); err != nil {
		t.Fatalf("Put: %v", err)
	}
	original.Registry.Accesses[0].Path = "/changed-after-put"

	got, err := store.Get(ctx, "run-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Registry.Accesses) != 1 || got.Registry.Accesses[0] != want || got.Registry.Count != 3 {
		t.Fatalf("registry after the writer's change = %+v, want [%+v] count 3", got.Registry, want)
	}
	got.Registry.Accesses[0].Path = "/changed-after-get"
	again, err := store.Get(ctx, "run-1")
	if err != nil {
		t.Fatalf("Get again: %v", err)
	}
	if again.Registry.Accesses[0] != want {
		t.Fatalf("registry after a reader's change = %+v, want %+v", again.Registry.Accesses[0], want)
	}
}

// stagedRun is a run with a finished stage, a stage in flight, and a gate
// report and an attempt's report whose streams, egress and registry lists
// a caller could reach.
func stagedRun() domain.Run {
	at := time.Date(2026, 10, 31, 12, 1, 0, 0, time.UTC)
	finished := at.Add(time.Minute)
	run := sampleRun("run-1")
	run.Stages = []domain.Stage{
		{Name: domain.StageClone, StartedAt: at, FinishedAt: &finished, Outcome: domain.StageSucceeded},
		{Name: domain.StageGates, StartedAt: finished, Outcome: domain.StageRunning},
	}
	run.Gates = stagedReport(at)
	run.Attempts = []domain.Attempt{{Number: 1, HeadSHA: "7d3f1e2a9b8c4d5e6f7a8b9c0d1e2f3a4b5c6d7e", Gates: stagedReport(at)}}
	run.PullRequest = domain.PullRequest{Number: 41, URL: "https://github.com/fuseone-io/fuseone-forja/pull/41"}
	return run
}

// stagedReport is a gate report of its own lists and streams on each call.
func stagedReport(at time.Time) domain.GateReport {
	return domain.GateReport{
		Setup: []domain.GateResult{{Name: "setup[0]", Stdout: []byte("setup out"), Stderr: []byte("setup err")}},
		Gates: []domain.GateResult{{Name: "build", Stdout: []byte("build out"), Stderr: []byte("build err")}},
		Egress: domain.EgressReport{
			Denied: []domain.EgressDenial{{At: at, Kind: domain.EgressConnect, Destination: "example.com:443"}}, Count: 1,
		},
		Registry: domain.RegistryReport{
			Accesses: []domain.RegistryAccess{{At: at, Registry: "ghcr.io/my-org", Path: "/pkg", Outcome: domain.RegistryAllowed, Status: 200}}, Count: 1,
		},
	}
}

// scribble changes, in place, everything of run a shared backing array or
// pointer would carry into the store.
func scribble(run domain.Run) {
	*run.Stages[0].FinishedAt = run.Stages[0].FinishedAt.Add(time.Hour)
	run.Stages[1].Outcome = domain.StageFailed
	run.Gates.Setup[0].Stdout[0] = 'X'
	run.Gates.Gates[0].Stderr[0] = 'X'
	run.Gates.Gates[0].ExitCode = 99
	run.Gates.Egress.Denied[0].Destination = "changed:443"
	run.Gates.Registry.Accesses[0].Path = "/changed"
	run.Attempts[0].HeadSHA = "changed"
	run.Attempts[0].Gates.Setup[0].Stdout[0] = 'X'
	run.Attempts[0].Gates.Gates[0].Stderr[0] = 'X'
	run.Attempts[0].Gates.Egress.Denied[0].Destination = "changed:443"
	run.Attempts[0].Gates.Registry.Accesses[0].Path = "/changed"
}

// The stages, the gate report and every attempt are evidence: neither the
// writer after Put nor a reader after Get can change what the store holds
// through a shared slice, byte stream or timestamp.
func test_stagesAndGateStreams_notSharedWithCaller(t *testing.T, open opener) {
	t.Parallel()
	ctx := context.Background()
	store := open(t)
	want := stagedRun()
	written := stagedRun()
	if err := store.Put(ctx, written); err != nil {
		t.Fatalf("Put: %v", err)
	}
	scribble(written)

	got, err := store.Get(ctx, "run-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	assertStagedRun(t, "after the writer's change", got, want)
	scribble(got)
	again, err := store.Get(ctx, "run-1")
	if err != nil {
		t.Fatalf("Get again: %v", err)
	}
	assertStagedRun(t, "after a reader's change", again, want)
}

func assertStagedRun(t *testing.T, when string, got, want domain.Run) {
	t.Helper()
	if !reflect.DeepEqual(got.Stages, want.Stages) || !reflect.DeepEqual(got.Gates, want.Gates) || got.PullRequest != want.PullRequest {
		t.Fatalf("%s: stages %+v gates %+v pr %+v, want %+v %+v %+v", when, got.Stages, got.Gates, got.PullRequest, want.Stages, want.Gates, want.PullRequest)
	}
	if !reflect.DeepEqual(got.Attempts, want.Attempts) {
		t.Fatalf("%s: attempts %+v, want %+v", when, got.Attempts, want.Attempts)
	}
}

// listedRun is sampleRun in repo, started offset after noon.
func listedRun(id, repo string, offset time.Duration) domain.Run {
	run := stagedRun()
	run.ID, run.Repository = id, repo
	run.StartedAt = run.StartedAt.Add(offset)
	run.Egress = domain.EgressReport{Denied: []domain.EgressDenial{{At: run.StartedAt, Kind: domain.EgressHTTP, Destination: "example.com:80"}}, Count: 1}
	return run
}

func listIDs(runs []domain.Run) []string {
	ids := make([]string, 0, len(runs))
	for _, r := range runs {
		ids = append(ids, r.ID)
	}
	return ids
}

// List returns the runs the filter matches, newest start first and, on the
// same start, highest id first; and what it returns is a copy.
func test_list_filtersSortsAndReturnsCopies(t *testing.T, open opener) {
	t.Parallel()
	ctx := context.Background()
	store := open(t)
	for _, r := range []domain.Run{
		listedRun("run-a", "acme/api", 0),
		listedRun("run-c", "acme/api", time.Hour),
		listedRun("run-b", "acme/api", time.Hour),
		listedRun("run-d", "acme/web", 2*time.Hour),
	} {
		if err := store.Put(ctx, r); err != nil {
			t.Fatalf("Put %s: %v", r.ID, err)
		}
	}

	got, err := store.List(ctx, domain.RunFilter{Repository: "acme/api"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if want := []string{"run-c", "run-b", "run-a"}; !reflect.DeepEqual(listIDs(got), want) {
		t.Fatalf("List = %v, want %v", listIDs(got), want)
	}
	all, err := store.List(ctx, domain.RunFilter{})
	if err != nil || len(all) != 4 || all[0].ID != "run-d" {
		t.Fatalf("List(zero filter) = %v, %v; want all four, run-d first", listIDs(all), err)
	}

	scribble(got[0])
	got[0].Egress.Denied[0].Destination = "changed:80"
	again, err := store.Get(ctx, "run-c")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	assertStagedRun(t, "after a lister's change", again, listedRun("run-c", "acme/api", time.Hour))
	if again.Egress.Denied[0].Destination != "example.com:80" {
		t.Fatalf("a lister's change reached the store's egress list: %+v", again.Egress.Denied)
	}
}

func test_list_noMatchEmptyNotNilAndInvalidFilterRejected(t *testing.T, open opener) {
	t.Parallel()
	ctx := context.Background()
	store := open(t)
	if err := store.Put(ctx, sampleRun("run-1")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := store.List(ctx, domain.RunFilter{Repository: "acme/none"})
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("List(no match) = %#v, %v; want an empty slice", got, err)
	}
	if _, err := store.List(ctx, domain.RunFilter{State: "paused"}); err == nil || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("List(unknown state) = %v, want an error naming it", err)
	}
	for _, f := range []domain.RunFilter{
		{Repository: "acme/api\x00"}, {Repository: "acme/\xffapi"}, {Client: "pilot\x00"}, {Client: "pilot\xff"},
	} {
		if _, err := store.List(ctx, f); err == nil {
			t.Errorf("List(%+q) accepted a filter no run can match", f)
		}
	}
}

func test_concurrentPutAndGet_noRace(t *testing.T, open opener) {
	t.Parallel()
	ctx := context.Background()
	store := open(t)

	const workers = 16
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			id := fmt.Sprintf("run-%d", i%4)
			for range 50 {
				if err := store.Put(ctx, sampleRun(id)); err != nil {
					t.Errorf("Put %s: %v", id, err)
					return
				}
				if _, err := store.Get(ctx, id); err != nil {
					t.Errorf("Get %s: %v", id, err)
					return
				}
				if _, err := store.List(ctx, domain.RunFilter{}); err != nil {
					t.Errorf("List: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()
}

// A run never changes hands: a record of an existing run under another
// client is refused and the run stays its first client's, while the same
// client may record it again.
func test_putExistingIDOtherClient_refusedRunKeepsItsClient(t *testing.T, open opener) {
	t.Parallel()
	ctx := context.Background()
	store := open(t)
	first := sampleRun("run-1")
	if err := store.Put(ctx, first); err != nil {
		t.Fatalf("Put first: %v", err)
	}
	taken := first
	taken.Client, taken.State = "fuseone-staging", domain.StateFailed
	if err := store.Put(ctx, taken); !errors.Is(err, domain.ErrRunClientChanged) {
		t.Fatalf("Put under another client = %v, want ErrRunClientChanged", err)
	}
	got, err := store.Get(ctx, "run-1")
	if err != nil || got.Client != first.Client || got.State != first.State {
		t.Fatalf("run = %+v, %v; want it unchanged, still %s's", got, err, first.Client)
	}
	again := first
	again.State = domain.StateFailed
	if err := store.Put(ctx, again); err != nil {
		t.Fatalf("Put by the same client: %v", err)
	}
	if got, err := store.Get(ctx, "run-1"); err != nil || got.State != domain.StateFailed {
		t.Fatalf("run = %+v, %v; want the same client's record applied", got, err)
	}
}

// List keeps one client's runs when the filter names it, compared
// exactly, and every client's under the zero filter.
func test_listByClient_onlyThatClientsRuns(t *testing.T, open opener) {
	t.Parallel()
	ctx := context.Background()
	store := open(t)
	mine, theirs, cased := sampleRun("run-mine"), sampleRun("run-theirs"), sampleRun("run-cased")
	theirs.Client, cased.Client = "fuseone-staging", "FuseOne-Pilot"
	for _, r := range []domain.Run{mine, theirs, cased} {
		if err := store.Put(ctx, r); err != nil {
			t.Fatalf("Put %s: %v", r.ID, err)
		}
	}
	got, err := store.List(ctx, domain.RunFilter{Client: mine.Client})
	if err != nil || !reflect.DeepEqual(listIDs(got), []string{"run-mine"}) {
		t.Fatalf("List(client %s) = %v, %v; want [run-mine]", mine.Client, listIDs(got), err)
	}
	if all, err := store.List(ctx, domain.RunFilter{}); err != nil || len(all) != 3 {
		t.Fatalf("List(zero filter) = %v, %v; want every client's runs", listIDs(all), err)
	}
}

// GetOwned answers a run only to the client it was recorded under: to any
// other client, and to no client, it is an id no run has, in the same
// words.
func test_getOwned_anotherClientsRunIsUnknown(t *testing.T, open opener) {
	t.Parallel()
	ctx := context.Background()
	store := open(t)
	run := sampleRun("run-1")
	if err := store.Put(ctx, run); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := store.GetOwned(ctx, run.Client, "run-1")
	if err != nil {
		t.Fatalf("GetOwned by its client: %v", err)
	}
	assertSameRun(t, got, run)
	_, unknown := store.GetOwned(ctx, "fuseone-staging", "run-2")
	for _, client := range []string{"fuseone-staging", "FuseOne-Pilot", "", "pilot\x00"} {
		_, err := store.GetOwned(ctx, client, "run-1")
		if !errors.Is(err, domain.ErrRunNotFound) || strings.ReplaceAll(err.Error(), "run-1", "run-2") != unknown.Error() {
			t.Fatalf("GetOwned(%q) = %v, want %v but for the id", client, err, unknown)
		}
	}
}
