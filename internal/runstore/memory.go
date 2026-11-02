// Package runstore keeps run state: what forja.status reads and the run
// engine writes.
package runstore

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

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

// List returns a copy of every run the filter matches, sorted with
// domain.CompareNewestFirst; empty, never nil, when none does. An invalid
// filter is an error.
func (m *Memory) List(_ context.Context, f domain.RunFilter) ([]domain.Run, error) {
	if err := f.Validate(); err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]domain.Run, 0)
	for _, run := range m.runs {
		if f.Match(run) {
			out = append(out, clone(run))
		}
	}
	slices.SortFunc(out, domain.CompareNewestFirst)
	return out, nil
}

// clone copies the run so that none of its pointers, lists or streams are
// shared between the store and its callers.
func clone(run domain.Run) domain.Run {
	run.FinishedAt = cloneTime(run.FinishedAt)
	if run.DiffLines != nil {
		lines := *run.DiffLines
		run.DiffLines = &lines
	}
	run.Egress.Denied = slices.Clone(run.Egress.Denied)
	run.Registry.Accesses = slices.Clone(run.Registry.Accesses)
	run.Stages = slices.Clone(run.Stages)
	for i := range run.Stages {
		run.Stages[i].FinishedAt = cloneTime(run.Stages[i].FinishedAt)
	}
	run.Gates = cloneReport(run.Gates)
	return run
}

func cloneReport(r domain.GateReport) domain.GateReport {
	r.Setup = cloneResults(r.Setup)
	r.Gates = cloneResults(r.Gates)
	r.Egress.Denied = slices.Clone(r.Egress.Denied)
	r.Registry.Accesses = slices.Clone(r.Registry.Accesses)
	return r
}

func cloneResults(results []domain.GateResult) []domain.GateResult {
	results = slices.Clone(results)
	for i := range results {
		results[i].Stdout = slices.Clone(results[i].Stdout)
		results[i].Stderr = slices.Clone(results[i].Stderr)
	}
	return results
}

func cloneTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}
