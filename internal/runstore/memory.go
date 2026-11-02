// Package runstore keeps run state: what forja.status reads and the run
// engine writes.
package runstore

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// Memory is an in-process run store. The zero value is ready to use.
type Memory struct {
	mu   sync.RWMutex
	runs map[string]domain.Run
}

// NewMemory returns an empty in-memory run store.
func NewMemory() *Memory {
	return &Memory{runs: make(map[string]domain.Run)}
}

// Put stores the run under its ID, replacing any run already there. An
// invalid run is rejected and leaves the store untouched.
func (m *Memory) Put(_ context.Context, run domain.Run) error {
	if err := run.Validate(); err != nil {
		return fmt.Errorf("put run: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runs == nil {
		m.runs = make(map[string]domain.Run)
	}
	m.runs[run.ID] = clone(run)
	return nil
}

// Get returns a copy of the run with the given ID, or an error wrapping
// domain.ErrRunNotFound.
func (m *Memory) Get(_ context.Context, id string) (domain.Run, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	run, ok := m.runs[id]
	if !ok {
		return domain.Run{}, fmt.Errorf("run %q: %w", id, domain.ErrRunNotFound)
	}
	return clone(run), nil
}

// clone copies the run so that neither the pointer field nor the denial
// and access lists are shared between the store and its callers.
func clone(run domain.Run) domain.Run {
	if run.FinishedAt != nil {
		finished := *run.FinishedAt
		run.FinishedAt = &finished
	}
	run.Egress.Denied = slices.Clone(run.Egress.Denied)
	run.Registry.Accesses = slices.Clone(run.Registry.Accesses)
	return run
}
