package mcp_test

import (
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	forja "github.com/fuseone-io/fuseone-forja/internal/mcp"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

// steppingClock advances a second on every reading, so two readings never
// tie and an order between them shows.
type steppingClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *steppingClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(time.Second)
	return c.at
}

var implementStages = []string{"claim", "clone", "agent", "gates", "diff", "push", "pull_request", "status", "release"}

func stagesOf(t *testing.T, wire map[string]json.RawMessage) []forja.StageReport {
	t.Helper()
	var stages []forja.StageReport
	if err := json.Unmarshal(wire["stages"], &stages); err != nil {
		t.Fatalf("stages = %s: %v", wire["stages"], err)
	}
	return stages
}

// forja.status shows each stage of an implement run with when it started
// and ended, in order; a stage still running has no end yet.
func TestStatus_implementRun_stagesInOrderWithTimestamps(t *testing.T) {
	t.Parallel()
	clock := &steppingClock{at: startedAt()}
	fx := newWiredFixture(t, contractWithWallClock, fixtureWiring{deps: func(d *forja.Deps) { d.Now = clock.now }})
	var midRun map[string]json.RawMessage
	fx.gates.during = func() { midRun = statusOf(t, fx) }

	decodeImplement(t, callImplement(t, fx.cs, implementIssue))
	mid := stagesOf(t, midRun)
	if last := mid[len(mid)-1]; last.Name != "gates" || last.Outcome != "running" || last.FinishedAt != nil {
		t.Fatalf("stage seen while the gates ran = %+v, want gates running with no finished_at", last)
	}
	if raw := string(midRun["stages"]); !json.Valid([]byte(raw)) || jsonHasKeyInLast(t, raw, "finished_at") {
		t.Fatalf("the running stage carries finished_at on the wire: %s", raw)
	}
	stages := stagesOf(t, statusOf(t, fx))
	names := make([]string, 0, len(stages))
	for _, s := range stages {
		names = append(names, s.Name)
	}
	if !reflect.DeepEqual(names, implementStages) {
		t.Fatalf("stages = %q, want %q", names, implementStages)
	}
	assertStagesTimed(t, stages)
}

// assertStagesTimed checks every stage ended after it started, succeeded,
// and started no earlier than the one before it ended.
func assertStagesTimed(t *testing.T, stages []forja.StageReport) {
	t.Helper()
	var prevEnd time.Time
	for _, s := range stages {
		if s.FinishedAt == nil || !s.StartedAt.Before(*s.FinishedAt) || s.Outcome != "succeeded" {
			t.Fatalf("stage %+v, want succeeded and finished after it started", s)
		}
		if s.StartedAt.Before(prevEnd) {
			t.Fatalf("stage %s started at %v, before the previous one ended at %v", s.Name, s.StartedAt, prevEnd)
		}
		prevEnd = *s.FinishedAt
	}
}

func jsonHasKeyInLast(t *testing.T, raw, key string) bool {
	t.Helper()
	var list []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &list); err != nil || len(list) == 0 {
		t.Fatalf("stages %s: %v", raw, err)
	}
	_, ok := list[len(list)-1][key]
	return ok
}

// A run with no stages, such as a doctor run, says so with an empty list,
// never a missing key or null.
func TestStatus_noStages_emptyListsOnTheWire(t *testing.T) {
	t.Parallel()
	store := runstore.NewMemory()
	mustPut(t, store, terminalRun("run-plain"))
	cs := connect(t, store)

	wire := statusWire(t, cs, "run-plain")
	for _, key := range []string{"stages", "setup", "gates"} {
		if string(wire[key]) != "[]" {
			t.Errorf("%s = %s, want []", key, wire[key])
		}
	}
	for _, key := range []string{"head_sha", "base_sha", "contract_sha", "pull_request"} {
		if _, present := wire[key]; present {
			t.Errorf("%s present on the wire of a run that has none: %s", key, wire[key])
		}
	}
}
