package workspace

import (
	"context"
	"testing"
	"time"
)

// A create of a live run is bounded by the run alone: no deadline of its
// own cuts a slow daemon short.
func TestCreateContext_runAlive_noDeadlineOfItsOwn(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	created, release, err := createContext(ctx, time.Millisecond)
	if err != nil {
		t.Fatalf("createContext: %v", err)
	}
	defer release()
	if deadline, ok := created.Deadline(); ok {
		t.Errorf("create deadline = %v, want none while the run is alive", deadline)
	}
	time.Sleep(20 * time.Millisecond)
	if err := created.Err(); err != nil {
		t.Errorf("create of a live run ended on its own: %v", err)
	}
}

// A create of a run with a deadline carries that deadline, grace later.
func TestCreateContext_runWithDeadline_deadlineMovedByTheGrace(t *testing.T) {
	t.Parallel()
	deadline := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	const grace = 7 * time.Second

	created, release, err := createContext(ctx, grace)
	if err != nil {
		t.Fatalf("createContext: %v", err)
	}
	defer release()
	if got, ok := created.Deadline(); !ok || !got.Equal(deadline.Add(grace)) {
		t.Errorf("create deadline = %v (set %v), want %v", got, ok, deadline.Add(grace))
	}
}

// The run's cancellation reaches a create in flight only after the grace,
// so the daemon's answer can still come back.
func TestCreateContext_runCancelled_createEndsAfterTheGrace(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	const grace = 500 * time.Millisecond

	created, release, err := createContext(ctx, grace)
	if err != nil {
		t.Fatalf("createContext: %v", err)
	}
	defer release()
	cancel()
	time.Sleep(grace * 4 / 5)
	if err := created.Err(); err != nil {
		t.Fatalf("create ended with the run, before the grace: %v", err)
	}
	select {
	case <-created.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("create still alive 10s after a 500ms grace")
	}
}

// A run that already ended sends no create.
func TestCreateContext_runEnded_refused(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := createContext(ctx, time.Second); err == nil {
		t.Fatal("createContext of an ended run = nil error, want the run's")
	}
}
