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
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

func sampleRun(id string) domain.Run {
	return domain.Run{
		ID:         id,
		Tool:       domain.ToolImplement,
		Repository: "fuseone-io/fuseone-forja",
		Issue:      5,
		State:      domain.StateRunning,
		StartedAt:  time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
	}
}

func TestMemory_putThenGet_returnsEqualRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := runstore.NewMemory()

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

func TestMemory_unknownID_errRunNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := runstore.NewMemory()
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

func TestMemory_putInvalidRun_rejectedAndNotStored(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(r *domain.Run)
	}{
		{name: "empty id", mutate: func(r *domain.Run) { r.ID = "" }},
		{name: "unknown tool", mutate: func(r *domain.Run) { r.Tool = "forja.deploy" }},
		{name: "unknown state", mutate: func(r *domain.Run) { r.State = "paused" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			store := runstore.NewMemory()
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

func TestMemory_putExistingID_replacesRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := runstore.NewMemory()

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

func TestMemory_getReturnsCopy_callerMutationNotVisibleOnNextGet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := runstore.NewMemory()

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

func TestMemory_putStoresCopy_callerMutationAfterPutNotVisible(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := runstore.NewMemory()

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
func TestMemory_get_egressListNotShared(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := runstore.NewMemory()
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

// The tokens and the reason are values: what Get returns is what was put,
// and a reader's copy is its own.
func TestMemory_tokensAndReason_roundTripAsValues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := runstore.NewMemory()
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
func TestMemory_limitsAndDiffLines_notSharedWithCaller(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := runstore.NewMemory()
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
func TestMemory_get_registryListNotShared(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := runstore.NewMemory()
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

// stagedRun is a run with a finished stage, a stage in flight and a gate
// report whose streams, egress and registry lists a caller could reach.
func stagedRun() domain.Run {
	at := time.Date(2026, 10, 31, 12, 1, 0, 0, time.UTC)
	finished := at.Add(time.Minute)
	run := sampleRun("run-1")
	run.Stages = []domain.Stage{
		{Name: domain.StageClone, StartedAt: at, FinishedAt: &finished, Outcome: domain.StageSucceeded},
		{Name: domain.StageGates, StartedAt: finished, Outcome: domain.StageRunning},
	}
	run.Gates = domain.GateReport{
		Setup: []domain.GateResult{{Name: "setup[0]", Stdout: []byte("setup out"), Stderr: []byte("setup err")}},
		Gates: []domain.GateResult{{Name: "build", Stdout: []byte("build out"), Stderr: []byte("build err")}},
		Egress: domain.EgressReport{
			Denied: []domain.EgressDenial{{At: at, Kind: domain.EgressConnect, Destination: "example.com:443"}}, Count: 1,
		},
		Registry: domain.RegistryReport{
			Accesses: []domain.RegistryAccess{{At: at, Registry: "ghcr.io/my-org", Path: "/pkg", Outcome: domain.RegistryAllowed, Status: 200}}, Count: 1,
		},
	}
	run.PullRequest = domain.PullRequest{Number: 41, URL: "https://github.com/fuseone-io/fuseone-forja/pull/41"}
	return run
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
}

// The stages and the gate report are evidence: neither the writer after
// Put nor a reader after Get can change what the store holds through a
// shared slice, byte stream or timestamp.
func TestMemory_stagesAndGateStreams_notSharedWithCaller(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := runstore.NewMemory()
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
}

func TestMemory_zeroValue_putThenGet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var store runstore.Memory

	if err := store.Put(ctx, sampleRun("run-1")); err != nil {
		t.Fatalf("Put on the zero value: %v", err)
	}
	got, err := store.Get(ctx, "run-1")
	if err != nil {
		t.Fatalf("Get on the zero value: %v", err)
	}
	assertSameRun(t, got, sampleRun("run-1"))
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
func TestMemory_list_filtersSortsAndReturnsCopies(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := runstore.NewMemory()
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

func TestMemory_list_noMatchEmptyNotNilAndInvalidFilterRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var store runstore.Memory

	got, err := store.List(ctx, domain.RunFilter{Repository: "acme/none"})
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("List on the zero value = %#v, %v; want an empty slice", got, err)
	}
	if _, err := store.List(ctx, domain.RunFilter{State: "paused"}); err == nil || !strings.Contains(err.Error(), "paused") {
		t.Fatalf("List(unknown state) = %v, want an error naming it", err)
	}
}

func TestMemory_concurrentPutAndGet_noRace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := runstore.NewMemory()

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
