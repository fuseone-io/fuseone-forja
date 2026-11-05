package runstore_test

import (
	"context"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

func TestMemory_behaviourSuite(t *testing.T) {
	t.Parallel()
	runSuite(t, func(*testing.T) store { return runstore.NewMemory() })
}

func TestMemory_zeroValue_putThenGetAndListEmptyNotNil(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var empty runstore.Memory
	if got, err := empty.List(ctx, domain.RunFilter{}); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("List on the zero value = %#v, %v; want an empty slice", got, err)
	}

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
