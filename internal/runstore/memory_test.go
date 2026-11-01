package runstore_test

import (
	"context"
	"errors"
	"fmt"
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
			}
		})
	}
	wg.Wait()
}
