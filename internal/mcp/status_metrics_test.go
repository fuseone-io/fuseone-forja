package mcp_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

// took is a run of repo in state that ended after d, with compute c.
func took(id, repo string, state domain.RunState, d, c time.Duration) domain.Run {
	run := listedRun(id, repo, domain.ToolImplement, state, 0)
	if run.FinishedAt != nil {
		finished := run.StartedAt.Add(d)
		run.FinishedAt = &finished
	}
	run.Compute = c
	return run
}

func failedWith(id string, reason domain.FailureReason, d time.Duration) domain.Run {
	run := took(id, "acme/api", domain.StateFailed, d, 20020*time.Millisecond)
	run.Reason = reason
	return run
}

// repositoriesWire calls forja.status with args and returns each
// repository object as it travelled.
func repositoriesWire(t *testing.T, cs *sdk.ClientSession, args map[string]any) []map[string]json.RawMessage {
	t.Helper()
	var repos []map[string]json.RawMessage
	raw := listWire(t, cs, args)["repositories"]
	if err := json.Unmarshal(raw, &repos); err != nil {
		t.Fatalf("repositories = %s: %v", raw, err)
	}
	return repos
}

func flat(obj map[string]json.RawMessage) map[string]string {
	out := map[string]string{}
	for k, v := range obj {
		out[k] = string(v)
	}
	return out
}

// flatList is a JSON array of objects as flat maps, in array order.
func flatList(t *testing.T, raw json.RawMessage) []map[string]string {
	t.Helper()
	var objs []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &objs); err != nil {
		t.Fatalf("%s is not an array of objects: %v", raw, err)
	}
	out := make([]map[string]string, 0, len(objs))
	for _, o := range objs {
		out = append(out, flat(o))
	}
	return out
}

func TestStatus_listRepositories_wireKeysAndFailureOrder(t *testing.T) {
	t.Parallel()
	web := took("web-1", "acme/web", domain.StateRunning, 0, 0)
	web.Tokens = domain.TokenUsage{Input: 5, Output: 6}
	store := storeWith(t,
		// Three finished runs of 1s, 2s and 2s average 1.667s; each used
		// 20.02s of compute, 0.334 minutes rounded alone and 1.001 summed.
		took("api-ok", "acme/api", domain.StateSucceeded, time.Second, 20020*time.Millisecond),
		failedWith("api-gate", domain.ReasonGateFailed, 2*time.Second),
		failedWith("api-none", "", 2*time.Second),
		took("api-queued", "acme/api", domain.StateQueued, 0, 0),
		web,
	)
	repos := repositoriesWire(t, connect(t, store), map[string]any{})
	if len(repos) != 2 {
		t.Fatalf("repositories = %v, want acme/api and acme/web", repos)
	}

	failures := repos[0]["failures"]
	delete(repos[0], "failures")
	wantAPI := map[string]string{
		"repository": `"acme/api"`, "runs": "4", "succeeded": "1", "failed": "2", "in_flight": "1",
		"average_duration_seconds": "1.667", "compute_minutes": "1.001", "tokens_in": "0", "tokens_out": "0",
		"egress_denied_count": "0", "egress_denied": "[]",
	}
	if got := flat(repos[0]); !reflect.DeepEqual(got, wantAPI) {
		t.Fatalf("acme/api = %v\nwant %v", got, wantAPI)
	}
	// One gate failure and one with no reason out of three finished runs.
	wantFailures := []map[string]string{
		{"reason": `"gate_failed"`, "count": "1", "rate": "0.3333"},
		{"reason": `"token_cap"`, "count": "0", "rate": "0"},
		{"reason": `"wall_clock"`, "count": "0", "rate": "0"},
		{"reason": `"diff_too_large"`, "count": "0", "rate": "0"},
		{"reason": `"no_commits"`, "count": "0", "rate": "0"},
		{"reason": `"inversion_unavailable"`, "count": "0", "rate": "0"},
		{"reason": `"unspecified"`, "count": "1", "rate": "0.3333"},
	}
	if got := flatList(t, failures); !reflect.DeepEqual(got, wantFailures) {
		t.Fatalf("failures = %v\nwant %v", got, wantFailures)
	}

	if string(repos[1]["tokens_in"]) != "5" || string(repos[1]["tokens_out"]) != "6" || string(repos[1]["in_flight"]) != "1" {
		t.Fatalf("acme/web = %v, want 5 tokens in, 6 out, 1 in flight", flat(repos[1]))
	}
	assertNothingFinished(t, repos[1])
}

// assertNothingFinished checks a repository none of whose runs finished:
// no average, and one failure row per reason, all at rate 0.
func assertNothingFinished(t *testing.T, repo map[string]json.RawMessage) {
	t.Helper()
	if raw, ok := repo["average_duration_seconds"]; ok {
		t.Fatalf("average_duration_seconds = %s with no finished run, want absent", raw)
	}
	rows := flatList(t, repo["failures"])
	if len(rows) != len(domain.FailureReasons()) {
		t.Fatalf("failures = %s, want %d rows", repo["failures"], len(domain.FailureReasons()))
	}
	for _, row := range rows {
		if row["rate"] != "0" {
			t.Fatalf("failures = %s, want every rate 0", repo["failures"])
		}
	}
}

// Each run's estimate is floored on its own; the repository's is their
// sum, so it is what FuseOne gets adding the runs up, not the estimate of
// the pooled usage.
func TestStatus_listRepositories_costIsSumOfRunEstimates_absentWithoutPrices(t *testing.T) {
	t.Parallel()
	runs := make([]domain.Run, 0, 3)
	for i := range 3 {
		run := took(fmt.Sprintf("run-%d", i), "acme/api", domain.StateSucceeded, time.Minute, 0)
		// 1 input at 2 000 000 micros per million and 3 cache reads at
		// 200 000 are 2.6 micros, 2 a run; the pooled usage would be 7.
		run.Tokens = domain.TokenUsage{CacheRead: 3, Input: 1}
		runs = append(runs, run)
	}
	store := storeWith(t, runs...)

	priced := listWire(t, connectPriced(t, store), map[string]any{})
	var listed []struct {
		Cost *int64 `json:"estimated_cost_usd_micros"`
	}
	if err := json.Unmarshal(priced["runs"], &listed); err != nil || len(listed) != 3 {
		t.Fatalf("runs = %s, %v", priced["runs"], err)
	}
	var sum int64
	for _, r := range listed {
		sum += *r.Cost
	}
	var repos []map[string]json.RawMessage
	if err := json.Unmarshal(priced["repositories"], &repos); err != nil || len(repos) != 1 {
		t.Fatalf("repositories = %s, %v", priced["repositories"], err)
	}
	if got, want := string(repos[0]["estimated_cost_usd_micros"]), fmt.Sprint(sum); got != want || want != "6" {
		t.Fatalf("repository estimate = %s, want %s, the sum of the runs' 2 each", got, want)
	}

	unpriced := repositoriesWire(t, connect(t, store), map[string]any{})
	if raw, ok := unpriced[0]["estimated_cost_usd_micros"]; ok {
		t.Fatalf("estimated_cost_usd_micros = %s with no price table, want absent", raw)
	}
}

// The totals are of every run the filter matches, not of the page, and
// every page repeats them.
func TestStatus_listRepositories_spanEveryPage(t *testing.T) {
	t.Parallel()
	store := runstore.NewMemory()
	for i := range 3 {
		mustPut(t, store, listedRun(fmt.Sprintf("run-%d", i), "acme/api", domain.ToolImplement, domain.StateFailed, time.Duration(i)*time.Hour))
	}
	mustPut(t, store, listedRun("run-ok", "acme/api", domain.ToolImplement, domain.StateSucceeded, 0))
	cs := connect(t, store)

	args := map[string]any{"state": "failed", "limit": 1}
	pages := 0
	for args != nil {
		list := listWire(t, cs, args)
		pages++
		var repos []map[string]json.RawMessage
		if err := json.Unmarshal(list["repositories"], &repos); err != nil || len(repos) != 1 {
			t.Fatalf("page %d repositories = %s, %v", pages, list["repositories"], err)
		}
		if string(repos[0]["runs"]) != "3" || string(repos[0]["failed"]) != "3" || string(repos[0]["succeeded"]) != "0" {
			t.Fatalf("page %d repository = %v, want the 3 failed runs the filter matches", pages, flat(repos[0]))
		}
		args = nextPage(t, list, map[string]any{"state": "failed", "limit": 1})
	}
	if pages != 3 {
		t.Fatalf("walk took %d pages, want 3", pages)
	}
}

// nextPage is args with the list's cursor, or nil on the last page.
func nextPage(t *testing.T, list map[string]json.RawMessage, args map[string]any) map[string]any {
	t.Helper()
	raw, ok := list["next_cursor"]
	if !ok {
		return nil
	}
	var cursor string
	if err := json.Unmarshal(raw, &cursor); err != nil {
		t.Fatalf("next_cursor = %s: %v", raw, err)
	}
	args["cursor"] = cursor
	return args
}

// deniedTo is a failed run started offset after startedAt whose gateway
// listed listed denials and counted count.
func deniedTo(id string, offset time.Duration, listed, count int) domain.Run {
	run := took(id, "acme/api", domain.StateFailed, time.Minute, 0)
	run.StartedAt = run.StartedAt.Add(offset)
	for i := range listed {
		run.Egress.Record(domain.EgressDenial{At: run.StartedAt.Add(time.Duration(i) * time.Second), Kind: domain.EgressConnect, Destination: fmt.Sprintf("%s-%d.example:443", id, i)})
	}
	run.Egress.Count = count
	return run
}

func TestStatus_listRepositories_egressDeniedCountAndEventsWithRunID(t *testing.T) {
	t.Parallel()
	older := deniedTo("run-older", 0, 1, 4)
	newer := deniedTo("run-newer", time.Hour, 1, 2)
	repos := repositoriesWire(t, connect(t, storeWith(t, older, newer)), map[string]any{})

	if string(repos[0]["egress_denied_count"]) != "6" {
		t.Fatalf("egress_denied_count = %s, want 6, every count summed", repos[0]["egress_denied_count"])
	}
	want := []map[string]string{
		{"run_id": `"run-newer"`, "at": `"2026-10-31T13:00:00Z"`, "kind": `"connect"`, "destination": `"run-newer-0.example:443"`},
		{"run_id": `"run-older"`, "at": `"2026-10-31T12:00:00Z"`, "kind": `"connect"`, "destination": `"run-older-0.example:443"`},
	}
	if got := flatList(t, repos[0]["egress_denied"]); !reflect.DeepEqual(got, want) {
		t.Fatalf("egress_denied = %v\nwant %v", got, want)
	}
}

func TestStatus_listRepositories_egressCapped_countKeepsRunning(t *testing.T) {
	t.Parallel()
	a := deniedTo("run-a", 0, 80, 80)
	b := deniedTo("run-b", time.Hour, 80, 95)
	repos := repositoriesWire(t, connect(t, storeWith(t, a, b)), map[string]any{})

	var events []struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(repos[0]["egress_denied"], &events); err != nil {
		t.Fatalf("egress_denied = %s: %v", repos[0]["egress_denied"], err)
	}
	if len(events) != domain.MaxEgressDenials || events[0].RunID != "run-b" || events[80].RunID != "run-a" {
		t.Fatalf("egress_denied lists %d events, want the cap %d, run-b's first", len(events), domain.MaxEgressDenials)
	}
	if string(repos[0]["egress_denied_count"]) != "175" {
		t.Fatalf("egress_denied_count = %s, want 175 past the cap", repos[0]["egress_denied_count"])
	}
}
