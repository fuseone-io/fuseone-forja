package mcp_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	forja "github.com/fuseone-io/fuseone-forja/internal/mcp"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

// listedRun is a run of repo by tool in state, started offset after
// startedAt, finished a minute later when terminal.
func listedRun(id, repo string, tool domain.Tool, state domain.RunState, offset time.Duration) domain.Run {
	run := domain.Run{ID: id, Tool: tool, Repository: repo, Issue: 7, State: state, StartedAt: startedAt().Add(offset)}
	if state == domain.StateSucceeded || state == domain.StateFailed {
		finished := run.StartedAt.Add(time.Minute)
		run.FinishedAt = &finished
	}
	return run
}

// listWire calls forja.status with args and returns the list object as it
// travelled, failing on a tool or protocol error.
func listWire(t *testing.T, cs *sdk.ClientSession, args map[string]any) map[string]json.RawMessage {
	t.Helper()
	res := callStatus(t, cs, args)
	if res.IsError {
		t.Fatalf("IsError = true for %v, content: %s", args, textOf(res))
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(structuredJSON(t, res), &wire); err != nil {
		t.Fatalf("decode wire object: %v", err)
	}
	var list map[string]json.RawMessage
	if err := json.Unmarshal(wire["list"], &list); err != nil {
		t.Fatalf("list = %s for %v: %v", wire["list"], args, err)
	}
	return list
}

func callList(t *testing.T, cs *sdk.ClientSession, args map[string]any) forja.RunList {
	t.Helper()
	res := callStatus(t, cs, args)
	if res.IsError {
		t.Fatalf("IsError = true for %v, content: %s", args, textOf(res))
	}
	var out forja.StatusOutput
	if err := json.Unmarshal(structuredJSON(t, res), &out); err != nil {
		t.Fatalf("decode StatusOutput: %v", err)
	}
	if out.List == nil {
		t.Fatalf("no list on the wire for %v: %s", args, structuredJSON(t, res))
	}
	return *out.List
}

func summaryIDs(list forja.RunList) []string {
	ids := []string{}
	for _, r := range list.Runs {
		ids = append(ids, r.ID)
	}
	return ids
}

// connectPriced is connect with testPrices as the server's price table.
func connectPriced(t *testing.T, store *runstore.Memory) *sdk.ClientSession {
	t.Helper()
	contracts := &fakeContracts{err: errors.New("no contract reader in this test")}
	claims := &fakeClaims{calls: &callLog{}, claimErr: errors.New("no claimer in this test")}
	deps := testDeps(store, contracts, claims)
	deps.Prices = &testPrices
	return connectDeps(t, deps)
}

func storeWith(t *testing.T, runs ...domain.Run) *runstore.Memory {
	t.Helper()
	store := runstore.NewMemory()
	for _, r := range runs {
		mustPut(t, store, r)
	}
	return store
}

func TestStatus_noRunID_listsRunsNewestFirst(t *testing.T) {
	t.Parallel()
	store := storeWith(t,
		listedRun("run-old", "acme/api", domain.ToolImplement, domain.StateSucceeded, 0),
		listedRun("run-new", "acme/web", domain.ToolDoctor, domain.StateRunning, 2*time.Hour),
		listedRun("run-mid-a", "acme/api", domain.ToolImplement, domain.StateFailed, time.Hour),
		listedRun("run-mid-b", "acme/api", domain.ToolImplement, domain.StateFailed, time.Hour),
	)
	cs := connect(t, store)

	got := callList(t, cs, map[string]any{})
	if want := []string{"run-new", "run-mid-b", "run-mid-a", "run-old"}; !reflect.DeepEqual(summaryIDs(got), want) {
		t.Fatalf("runs = %v, want %v", summaryIDs(got), want)
	}
	if got.NextCursor != "" {
		t.Fatalf("next_cursor = %q on the only page, want absent", got.NextCursor)
	}
}

// A listed run carries what the platform owner adds up, and none of what
// stays by id: no stages, no gate logs, no SHAs.
func TestStatus_listRunSummary_wireKeysAndValues(t *testing.T) {
	t.Parallel()
	run := listedRun("run-1", "acme/api", domain.ToolImplement, domain.StateFailed, 0)
	run.Reason, run.Tokens, run.Compute = domain.ReasonGateFailed, spent, 90*time.Second
	run.Egress = domain.EgressReport{Denied: []domain.EgressDenial{{At: run.StartedAt, Kind: domain.EgressHTTP, Destination: "example.com:80"}}, Count: 3}
	run.BaseSHA, run.HeadSHA = "aaa", "bbb"
	run.Gates = domain.GateReport{Gates: []domain.GateResult{{Name: "test", Stdout: []byte("secret log")}}}
	cs := connectPriced(t, storeWith(t, run))

	var runs []map[string]json.RawMessage
	if err := json.Unmarshal(listWire(t, cs, map[string]any{})["runs"], &runs); err != nil || len(runs) != 1 {
		t.Fatalf("runs = %v, %v; want one", runs, err)
	}
	got := map[string]string{}
	for k, v := range runs[0] {
		got[k] = string(v)
	}
	want := map[string]string{
		"id": `"run-1"`, "tool": `"forja.implement"`, "repository": `"acme/api"`, "issue": "7", "state": `"failed"`,
		"started_at": `"2026-10-31T12:00:00Z"`, "finished_at": `"2026-10-31T12:01:00Z"`, "failure_reason": `"gate_failed"`,
		"tokens_in": "1230", "tokens_out": "400", "compute_minutes": "1.5",
		// 6 506 for the tokens and 90 000 ms · 40 000 / 60 000 = 60 000.
		"estimated_cost_usd_micros": "66506", "egress_denied_count": "3",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("listed run = %v\nwant %v", got, want)
	}
}

func TestStatus_listRunSummary_inFlightNoPrices_optionalKeysAbsent(t *testing.T) {
	t.Parallel()
	cs := connect(t, storeWith(t, listedRun("run-1", "acme/api", domain.ToolImplement, domain.StateRunning, 0)))

	var runs []map[string]json.RawMessage
	if err := json.Unmarshal(listWire(t, cs, map[string]any{})["runs"], &runs); err != nil || len(runs) != 1 {
		t.Fatalf("runs = %v, %v; want one", runs, err)
	}
	for _, key := range []string{"finished_at", "failure_reason", "estimated_cost_usd_micros"} {
		if raw, ok := runs[0][key]; ok {
			t.Errorf("%s = %s on a running run with no price table, want absent", key, raw)
		}
	}
	if len(runs[0]) != 10 {
		t.Errorf("listed run has %d keys, want the 10 always present: %v", len(runs[0]), runs[0])
	}
}

func filterFixture() []domain.Run {
	return []domain.Run{
		listedRun("api-impl-ok", "acme/api", domain.ToolImplement, domain.StateSucceeded, 0),
		listedRun("api-impl-fail", "acme/api", domain.ToolImplement, domain.StateFailed, time.Hour),
		listedRun("api-doctor", "acme/api", domain.ToolDoctor, domain.StateSucceeded, 2*time.Hour),
		listedRun("web-impl-run", "acme/web", domain.ToolImplement, domain.StateRunning, 3*time.Hour),
		listedRun("api-impl-fail-late", "acme/api", domain.ToolImplement, domain.StateFailed, 4*time.Hour),
	}
}

func rfc3339(offset time.Duration) string {
	return startedAt().Add(offset).Format(time.RFC3339)
}

func TestStatus_listFilters_matchOnlyThoseRuns(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args map[string]any
		want []string
	}{
		{name: "repository", args: map[string]any{"repository": "acme/web"}, want: []string{"web-impl-run"}},
		{name: "tool", args: map[string]any{"tool": "forja.doctor"}, want: []string{"api-doctor"}},
		{name: "state", args: map[string]any{"state": "failed"}, want: []string{"api-impl-fail-late", "api-impl-fail"}},
		{name: "since is inclusive", args: map[string]any{"since": rfc3339(3 * time.Hour)}, want: []string{"api-impl-fail-late", "web-impl-run"}},
		{name: "until is exclusive", args: map[string]any{"until": rfc3339(time.Hour)}, want: []string{"api-impl-ok"}},
		{name: "since with a zone offset", args: map[string]any{"since": startedAt().Add(4 * time.Hour).In(time.FixedZone("BRT", -3*3600)).Format(time.RFC3339)}, want: []string{"api-impl-fail-late"}},
		{name: "combined", args: map[string]any{
			"repository": "acme/api", "tool": "forja.implement", "state": "failed",
			"since": rfc3339(time.Hour), "until": rfc3339(4 * time.Hour),
		}, want: []string{"api-impl-fail"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cs := connect(t, storeWith(t, filterFixture()...))
			if got := summaryIDs(callList(t, cs, tt.args)); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("runs = %v, want %v", got, tt.want)
			}
		})
	}
}

// walk pages through forja.status with limit until no cursor is left,
// calling between after the first page, and returns every id it saw.
func walk(t *testing.T, cs *sdk.ClientSession, limit int, between func()) (ids []string, pages int) {
	t.Helper()
	args := map[string]any{"limit": limit}
	for {
		page := callList(t, cs, args)
		if len(page.Runs) > limit {
			t.Fatalf("page %d has %d runs, over the limit %d", pages, len(page.Runs), limit)
		}
		ids = append(ids, summaryIDs(page)...)
		pages++
		if page.NextCursor == "" {
			return ids, pages
		}
		if page.NextCursor != page.Runs[len(page.Runs)-1].ID {
			t.Fatalf("next_cursor = %q, want the page's last run %q", page.NextCursor, page.Runs[len(page.Runs)-1].ID)
		}
		if pages == 1 {
			between()
		}
		args = map[string]any{"limit": limit, "cursor": page.NextCursor}
	}
}

func TestStatus_pagination_limitAndCursor_walkEveryRunExactlyOnce(t *testing.T) {
	t.Parallel()
	store := runstore.NewMemory()
	var want []string
	// Pairs of runs share a start, so the cursor must break ties by id.
	for i := 6; i >= 0; i-- {
		id := fmt.Sprintf("run-%d", i)
		mustPut(t, store, listedRun(id, "acme/api", domain.ToolImplement, domain.StateSucceeded, time.Duration(i/2)*time.Hour))
		want = append(want, id)
	}
	cs := connect(t, store)

	got, pages := walk(t, cs, 3, func() {
		mustPut(t, store, listedRun("run-arrived", "acme/api", domain.ToolImplement, domain.StateRunning, 24*time.Hour))
	})
	if !reflect.DeepEqual(got, want) || pages != 3 {
		t.Fatalf("walk = %v in %d pages, want %v in 3", got, pages, want)
	}
}

func TestStatus_pagination_defaultLimitFiftyMaximumTwoHundred(t *testing.T) {
	t.Parallel()
	store := runstore.NewMemory()
	for i := range 201 {
		mustPut(t, store, listedRun(fmt.Sprintf("run-%03d", i), "acme/api", domain.ToolImplement, domain.StateSucceeded, time.Duration(i)*time.Second))
	}
	cs := connect(t, store)

	if got := callList(t, cs, map[string]any{}); len(got.Runs) != 50 || got.NextCursor != "run-151" {
		t.Fatalf("no limit: %d runs, next_cursor %q; want 50 and run-151", len(got.Runs), got.NextCursor)
	}
	if got := callList(t, cs, map[string]any{"limit": 200}); len(got.Runs) != 200 || got.NextCursor != "run-001" {
		t.Fatalf("limit 200: %d runs, next_cursor %q; want 200 and run-001", len(got.Runs), got.NextCursor)
	}
	if got := callList(t, cs, map[string]any{"limit": 1, "cursor": "run-001"}); !reflect.DeepEqual(summaryIDs(got), []string{"run-000"}) || got.NextCursor != "" {
		t.Fatalf("last page: %v, next_cursor %q; want [run-000] and none", summaryIDs(got), got.NextCursor)
	}
}

// A cursor no matching run comes after ends the walk: a run that left the
// filter between two pages, or the last run itself, never restarts it.
func TestStatus_pagination_cursorPastEveryMatch_emptyPageNoCursor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args map[string]any
	}{
		{name: "cursor on the oldest run", args: map[string]any{"cursor": "run-old"}},
		{name: "cursor older than every match, outside the filter", args: map[string]any{"state": "running", "cursor": "run-old"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cs := connect(t, storeWith(t,
				listedRun("run-old", "acme/api", domain.ToolImplement, domain.StateSucceeded, 0),
				listedRun("run-mid", "acme/api", domain.ToolImplement, domain.StateRunning, time.Hour),
				listedRun("run-new", "acme/api", domain.ToolImplement, domain.StateRunning, 2*time.Hour),
			))
			got := callList(t, cs, tt.args)
			if len(got.Runs) != 0 || got.NextCursor != "" {
				t.Fatalf("page after the last match = %v, next_cursor %q; want none and none", summaryIDs(got), got.NextCursor)
			}
		})
	}
}

func TestStatus_listInvalidInput_toolErrorNamingTheArgument(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args map[string]any
		want []string
	}{
		{name: "unknown tool", args: map[string]any{"tool": "forja.deploy"}, want: []string{"tool", "forja.deploy"}},
		{name: "unknown state", args: map[string]any{"state": "paused"}, want: []string{"state", "paused"}},
		{name: "malformed since", args: map[string]any{"since": "yesterday"}, want: []string{"since", "yesterday"}},
		{name: "malformed until", args: map[string]any{"until": "2026-13-01"}, want: []string{"until", "2026-13-01"}},
		{name: "since equal to until", args: map[string]any{"since": rfc3339(0), "until": rfc3339(0)}, want: []string{"since", "until"}},
		{name: "since after until", args: map[string]any{"since": rfc3339(time.Hour), "until": rfc3339(0)}, want: []string{"since", "until"}},
		{name: "limit over the maximum", args: map[string]any{"limit": 201}, want: []string{"limit", "201"}},
		{name: "negative limit", args: map[string]any{"limit": -1}, want: []string{"limit", "-1"}},
		{name: "unknown cursor", args: map[string]any{"cursor": "run-ghost"}, want: []string{"cursor", "run-ghost"}},
		{name: "run_id with a repository", args: map[string]any{"run_id": "run-present", "repository": "acme/api"}, want: []string{"run_id", "repository"}},
		{name: "run_id with a tool", args: map[string]any{"run_id": "run-present", "tool": "forja.implement"}, want: []string{"run_id", "tool"}},
		{name: "run_id with a state", args: map[string]any{"run_id": "run-present", "state": "running"}, want: []string{"run_id", "state"}},
		{name: "run_id with a since", args: map[string]any{"run_id": "run-present", "since": rfc3339(0)}, want: []string{"run_id", "since"}},
		{name: "run_id with an until", args: map[string]any{"run_id": "run-present", "until": rfc3339(time.Hour)}, want: []string{"run_id", "until"}},
		{name: "run_id with a cursor", args: map[string]any{"run_id": "run-present", "cursor": "run-present"}, want: []string{"run_id", "cursor"}},
		{name: "run_id with a limit", args: map[string]any{"run_id": "run-present", "limit": 5}, want: []string{"run_id", "limit"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cs := connect(t, storeWith(t, runningRun("run-present")))
			res := callStatus(t, cs, tt.args)
			if !res.IsError {
				t.Fatalf("IsError = false for %v; content: %s", tt.args, textOf(res))
			}
			for _, w := range tt.want {
				if !strings.Contains(textOf(res), w) {
					t.Errorf("error %q does not name %q", textOf(res), w)
				}
			}
		})
	}
}

func TestStatus_listNoMatch_emptyArraysNeverNull(t *testing.T) {
	t.Parallel()
	tests := map[string]*runstore.Memory{
		"empty store":   runstore.NewMemory(),
		"nothing match": storeWith(t, filterFixture()...),
	}
	for name, store := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			list := listWire(t, connect(t, store), map[string]any{"repository": "acme/none"})
			if string(list["runs"]) != "[]" || string(list["repositories"]) != "[]" {
				t.Fatalf("runs = %s, repositories = %s; want [] and []", list["runs"], list["repositories"])
			}
			if raw, ok := list["next_cursor"]; ok {
				t.Fatalf("next_cursor = %s with nothing listed, want absent", raw)
			}
		})
	}
}

// Listing is a second shape of the answer, not a change to the first: by
// id the wire still has exactly the run's keys, and no list.
func TestStatus_byID_wireUnchangedByList(t *testing.T) {
	t.Parallel()
	cs := connect(t, storeWith(t, filterFixture()...))

	wire := statusWire(t, cs, "api-impl-ok")
	if raw, ok := wire["list"]; ok {
		t.Fatalf("list = %s on a by-id answer, want absent", raw)
	}
	if len(wire) != 21 {
		t.Fatalf("by-id wire has %d keys, want 21: %v", len(wire), wire)
	}
	if string(wire["id"]) != `"api-impl-ok"` {
		t.Fatalf("id = %s, want the run asked for", wire["id"])
	}
}
