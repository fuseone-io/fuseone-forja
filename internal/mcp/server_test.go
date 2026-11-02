package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/github"
	forja "github.com/fuseone-io/fuseone-forja/internal/mcp"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

// connect wires a server over in-memory transports with a contract reader,
// a claimer, GitHub readers and a gates runner no test here calls, and
// closes both ends when the test ends.
func connect(t *testing.T, store *runstore.Memory) *sdk.ClientSession {
	t.Helper()
	return connectWith(t, store, &fakeCloner{err: errors.New("no cloner in this test")}, &fakeGates{err: errors.New("no gates runner in this test")})
}

// connectWith is connect with the dependencies forja.doctor reaches.
func connectWith(t *testing.T, store *runstore.Memory, cloner *fakeCloner, gates *fakeGates) *sdk.ClientSession {
	t.Helper()
	contracts := &fakeContracts{err: errors.New("no contract reader in this test")}
	claims := &fakeClaims{calls: &callLog{}, claimErr: errors.New("no claimer in this test")}
	deps := testDeps(store, contracts, claims)
	deps.Cloner = cloner
	deps.Gates = gates
	return connectDeps(t, deps)
}

// testDeps is the wiring every test starts from: a fixed clock, sequential
// run ids, and GitHub readers and a gates runner that refuse every call.
func testDeps(store forja.RunStore, contracts forja.ContractReader, claims forja.Claimer) forja.Deps {
	ids := &sequence{}
	return forja.Deps{
		Runs: store, Contracts: contracts, Claims: claims,
		Cloner: &fakeCloner{err: errors.New("no cloner in this test")},
		Issues: &fakeIssues{err: errors.New("no issue reader in this test")},
		Gates:  &fakeGates{err: errors.New("no gates runner in this test")},
		Agent:  &fakeAgent{err: errors.New("no agent in this test")},
		NewID:  ids.next, Now: startedAt, Version: "test",
		Pusher:            &fakePusher{deliveryLog[github.PushRequest]{err: errors.New("no pusher in this test")}},
		PullRequests:      &fakePullRequests{deliveryLog: deliveryLog[github.PullRequestRequest]{err: errors.New("no pull requests in this test")}},
		Statuses:          &fakeStatuses{deliveryLog[github.StatusRequest]{err: errors.New("no statuses in this test")}},
		BranchCloner:      &fakeBranchCloner{err: errors.New("no branch cloner in this test")},
		Identity:          &fakeIdentity{err: errors.New("no identity in this test")},
		PullRequestReader: &fakePullReader{err: errors.New("no pull request reader in this test")},
	}
}

func connectDeps(t *testing.T, deps forja.Deps) *sdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	server := forja.New(deps)
	clientTransport, serverTransport := sdk.NewInMemoryTransports()

	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "forja-test-client", Version: "test"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	})
	return clientSession
}

func startedAt() time.Time {
	return time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
}

func runningRun(id string) domain.Run {
	return domain.Run{
		ID:         id,
		Tool:       domain.ToolImplement,
		Repository: "fuseone-io/fuseone-forja",
		Issue:      5,
		State:      domain.StateRunning,
		StartedAt:  startedAt(),
	}
}

func terminalRun(id string) domain.Run {
	finished := startedAt().Add(7 * time.Minute)
	run := runningRun(id)
	run.State = domain.StateSucceeded
	run.FinishedAt = &finished
	return run
}

func mustPut(t *testing.T, store *runstore.Memory, run domain.Run) {
	t.Helper()
	if err := store.Put(context.Background(), run); err != nil {
		t.Fatalf("Put: %v", err)
	}
}

// callStatus takes the arguments as map[string]any because that is the type
// sdk.CallToolParams.Arguments carries on the wire.
func callStatus(t *testing.T, cs *sdk.ClientSession, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      string(domain.ToolStatus),
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool(forja.status): protocol error %v", err)
	}
	return res
}

func textOf(res *sdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// structuredJSON re-encodes the client's view of StructuredContent so that
// tests look at exactly what travelled on the wire.
func structuredJSON(t *testing.T, res *sdk.CallToolResult) []byte {
	t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal StructuredContent: %v", err)
	}
	return raw
}

func TestServer_listTools_fiveToolsWithSchemasAndClass(t *testing.T) {
	t.Parallel()
	cs := connect(t, runstore.NewMemory())

	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(res.Tools) != 5 {
		t.Fatalf("ListTools returned %d tools, want 5", len(res.Tools))
	}

	readOnly := map[domain.Tool]bool{
		domain.ToolDoctor:    true,
		domain.ToolPlan:      true,
		domain.ToolImplement: false,
		domain.ToolVerify:    false,
		domain.ToolStatus:    true,
	}
	seen := map[domain.Tool]bool{}
	for _, tool := range res.Tools {
		name := domain.Tool(tool.Name)
		wantRead, known := readOnly[name]
		if !known {
			t.Errorf("unexpected tool %q", tool.Name)
			continue
		}
		seen[name] = true
		assertSchemasAndClass(t, tool, wantRead)
	}
	if len(seen) != 5 {
		t.Fatalf("saw tools %v, want all five", seen)
	}
}

// assertSchemasAndClass checks one listed tool: both schemas present and the
// annotations spelling the Gate class exactly as the PRD table does.
func assertSchemasAndClass(t *testing.T, tool *sdk.Tool, wantRead bool) {
	t.Helper()
	if tool.InputSchema == nil || tool.OutputSchema == nil {
		t.Errorf("%s: input schema %v, output schema %v; both must be present", tool.Name, tool.InputSchema, tool.OutputSchema)
	}
	if tool.Annotations == nil {
		t.Errorf("%s: no annotations, class is unreadable", tool.Name)
		return
	}
	if tool.Annotations.ReadOnlyHint != wantRead {
		t.Errorf("%s: ReadOnlyHint = %v, want %v", tool.Name, tool.Annotations.ReadOnlyHint, wantRead)
	}
	if wantRead {
		return
	}
	if tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
		t.Errorf("%s: write tool must carry DestructiveHint = true, got %v", tool.Name, tool.Annotations.DestructiveHint)
	}
}

func TestStatus_existingRun_returnsItsFields(t *testing.T) {
	t.Parallel()
	store := runstore.NewMemory()
	want := terminalRun("run-42")
	mustPut(t, store, want)
	cs := connect(t, store)

	res := callStatus(t, cs, map[string]any{"run_id": "run-42"})
	if res.IsError {
		t.Fatalf("IsError = true, content: %s", textOf(res))
	}
	var got forja.StatusOutput
	if err := json.Unmarshal(structuredJSON(t, res), &got); err != nil {
		t.Fatalf("decode StatusOutput: %v", err)
	}
	assertStatusMatchesRun(t, got, want)
}

// assertStatusMatchesRun compares the wire form field by field with the run
// the store holds, including the pointer field.
func assertStatusMatchesRun(t *testing.T, got forja.StatusOutput, want domain.Run) {
	t.Helper()
	scalarsMatch := got.ID == want.ID &&
		got.Tool == string(want.Tool) &&
		got.Repository == want.Repository &&
		got.Issue == want.Issue &&
		got.State == string(want.State) &&
		got.StartedAt.Equal(want.StartedAt)
	if !scalarsMatch {
		t.Fatalf("StatusOutput = %+v, want run %+v", got, want)
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

func TestStatus_finishedAt_absentWhileRunningPresentWhenTerminal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		run  domain.Run
	}{
		{name: "running", run: runningRun("run-running")},
		{name: "terminal", run: terminalRun("run-terminal")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := runstore.NewMemory()
			mustPut(t, store, tt.run)
			cs := connect(t, store)

			res := callStatus(t, cs, map[string]any{"run_id": tt.run.ID})
			if res.IsError {
				t.Fatalf("IsError = true, content: %s", textOf(res))
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(structuredJSON(t, res), &wire); err != nil {
				t.Fatalf("decode wire object: %v", err)
			}
			raw, present := wire["finished_at"]
			if tt.run.FinishedAt == nil {
				if present {
					t.Fatalf("finished_at present on the wire for a running run: %s", raw)
				}
				return
			}
			if !present {
				t.Fatal("finished_at absent on the wire for a terminal run")
			}
			var gotTime string
			if err := json.Unmarshal(raw, &gotTime); err != nil {
				t.Fatalf("finished_at is not a JSON string: %s", raw)
			}
			if want := tt.run.FinishedAt.Format(time.RFC3339); gotTime != want {
				t.Fatalf("finished_at = %q, want %q", gotTime, want)
			}
		})
	}
}

func TestStatus_unknownRun_toolErrorNamesTheID(t *testing.T) {
	t.Parallel()
	store := runstore.NewMemory()
	mustPut(t, store, runningRun("run-present"))
	cs := connect(t, store)

	res := callStatus(t, cs, map[string]any{"run_id": "run-ghost"})
	if !res.IsError {
		t.Fatalf("IsError = false for an unknown run; content: %s", textOf(res))
	}
	text := textOf(res)
	if !strings.Contains(text, "run-ghost") {
		t.Errorf("error text %q does not name the id", text)
	}
	if !strings.Contains(text, "not found") {
		t.Errorf("error text %q does not say not found", text)
	}
}

func TestStatus_wireKeys_snakeCase(t *testing.T) {
	t.Parallel()
	store := runstore.NewMemory()
	mustPut(t, store, terminalRun("run-keys"))
	cs := connect(t, store)

	res := callStatus(t, cs, map[string]any{"run_id": "run-keys"})
	if res.IsError {
		t.Fatalf("IsError = true, content: %s", textOf(res))
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(structuredJSON(t, res), &wire); err != nil {
		t.Fatalf("decode wire object: %v", err)
	}
	want := []string{"id", "tool", "repository", "issue", "state", "started_at", "finished_at", "egress_denied_count", "egress_denied", "registry_access_count", "registry_access", "tokens_in", "tokens_out", "compute_minutes", "stages", "setup", "gates", "excluded", "human_merge", "proof_apparatus", "human_merge_paths"}
	for _, key := range want {
		if _, ok := wire[key]; !ok {
			t.Errorf("key %q absent on the wire", key)
		}
	}
	if len(wire) != len(want) {
		t.Errorf("wire object has %d keys, want %d: %s", len(wire), len(want), structuredJSON(t, res))
	}
}

// statusWire calls forja.status for id and returns the wire object.
func statusWire(t *testing.T, cs *sdk.ClientSession, id string) map[string]json.RawMessage {
	t.Helper()
	res := callStatus(t, cs, map[string]any{"run_id": id})
	if res.IsError {
		t.Fatalf("IsError = true, content: %s", textOf(res))
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(structuredJSON(t, res), &wire); err != nil {
		t.Fatalf("decode wire object: %v", err)
	}
	return wire
}

// The denials a doctor run's gateway recorded reach forja.status as the
// control plane read them: the exact count, and each listed attempt with
// its time, kind and host:port.
func TestStatus_doctorRunWithDenials_countAndEventsOnTheWire(t *testing.T) {
	t.Parallel()
	store := runstore.NewMemory()
	report := greenReport()
	report.Egress = deniedEgress()
	cs := connectWith(t, store, &fakeCloner{co: doctorCheckout(doctorContract)}, &fakeGates{report: report})
	runID := decodeDoctor(t, callDoctor(t, cs, doctorRepo)).RunID

	wire := statusWire(t, cs, runID)
	if string(wire["egress_denied_count"]) != "5" {
		t.Fatalf("egress_denied_count = %s, want 5", wire["egress_denied_count"])
	}
	var events []map[string]json.RawMessage
	if err := json.Unmarshal(wire["egress_denied"], &events); err != nil {
		t.Fatalf("egress_denied = %s: %v", wire["egress_denied"], err)
	}
	want := []map[string]string{
		{"at": `"2026-10-31T12:01:30Z"`, "kind": `"http"`, "destination": `"example.com:80"`},
		{"at": `"2026-10-31T12:01:31Z"`, "kind": `"connect"`, "destination": `"example.com:443"`},
	}
	if len(events) != len(want) {
		t.Fatalf("egress_denied = %s, want %d events", wire["egress_denied"], len(want))
	}
	for i, ev := range events {
		got := map[string]string{}
		for k, v := range ev {
			got[k] = string(v)
		}
		if !reflect.DeepEqual(got, want[i]) {
			t.Errorf("egress_denied[%d] = %v, want %v", i, got, want[i])
		}
	}
}

// A run nothing was denied for says so with a zero and an empty list,
// never a missing key or null.
func TestStatus_noDenials_zeroAndEmptyListOnTheWire(t *testing.T) {
	t.Parallel()
	store := runstore.NewMemory()
	mustPut(t, store, terminalRun("run-quiet"))
	cs := connect(t, store)

	wire := statusWire(t, cs, "run-quiet")
	if string(wire["egress_denied_count"]) != "0" || string(wire["egress_denied"]) != "[]" {
		t.Fatalf("egress_denied_count = %s, egress_denied = %s; want 0 and []", wire["egress_denied_count"], wire["egress_denied"])
	}
}

// registryAccess is what a registry proxy reports for a doctor run: more
// requests counted than listed, so a reader that counted the list would
// be caught.
func registryAccess() domain.RegistryReport {
	at := startedAt().Add(30 * time.Second)
	return domain.RegistryReport{
		Accesses: []domain.RegistryAccess{
			{At: at, Registry: "proxy.golang.org", Path: "/github.com/x/@v/list", Outcome: domain.RegistryAllowed, Status: 200},
			{At: at.Add(time.Second), Registry: "example.com", Path: "/x", Outcome: domain.RegistryDenied, Status: 403},
		},
		Count: 4,
	}
}

// What the doctor run's registry proxy served and refused reaches
// forja.status as the control plane read it; a run with none says so with
// a zero and an empty list, never a missing key or null.
func TestStatus_doctorRun_showsRegistryAccess(t *testing.T) {
	t.Parallel()
	store := runstore.NewMemory()
	report := greenReport()
	report.Registry = registryAccess()
	cs := connectWith(t, store, &fakeCloner{co: doctorCheckout(doctorContract)}, &fakeGates{report: report})
	runID := decodeDoctor(t, callDoctor(t, cs, doctorRepo)).RunID

	wire := statusWire(t, cs, runID)
	if string(wire["registry_access_count"]) != "4" {
		t.Fatalf("registry_access_count = %s, want 4", wire["registry_access_count"])
	}
	var events []map[string]json.RawMessage
	if err := json.Unmarshal(wire["registry_access"], &events); err != nil {
		t.Fatalf("registry_access = %s: %v", wire["registry_access"], err)
	}
	want := []map[string]string{
		{"at": `"2026-10-31T12:00:30Z"`, "registry": `"proxy.golang.org"`, "path": `"/github.com/x/@v/list"`, "outcome": `"allowed"`, "status": "200"},
		{"at": `"2026-10-31T12:00:31Z"`, "registry": `"example.com"`, "path": `"/x"`, "outcome": `"denied"`, "status": "403"},
	}
	if len(events) != len(want) {
		t.Fatalf("registry_access = %s, want %d events", wire["registry_access"], len(want))
	}
	for i, ev := range events {
		got := map[string]string{}
		for k, v := range ev {
			got[k] = string(v)
		}
		if !reflect.DeepEqual(got, want[i]) {
			t.Errorf("registry_access[%d] = %v, want %v", i, got, want[i])
		}
	}

	mustPut(t, store, terminalRun("run-no-registry"))
	quiet := statusWire(t, cs, "run-no-registry")
	if string(quiet["registry_access_count"]) != "0" || string(quiet["registry_access"]) != "[]" {
		t.Fatalf("registry_access_count = %s, registry_access = %s; want 0 and []", quiet["registry_access_count"], quiet["registry_access"])
	}
}

// A pull request GitHub cannot serve is a tool error naming the tool and
// the reader's failure, never a protocol error.
func TestVerify_readerFails_toolErrorNotProtocolError(t *testing.T) {
	t.Parallel()
	cs := connect(t, runstore.NewMemory())

	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      string(domain.ToolVerify),
		Arguments: map[string]any{"repository": "fuseone-io/fuseone-forja", "pull_request": 9},
	})
	if err != nil {
		t.Fatalf("CallTool(forja.verify): protocol error %v, want a tool error", err)
	}
	text := textOf(res)
	if !res.IsError || !strings.Contains(text, string(domain.ToolVerify)) || !strings.Contains(text, "no pull request reader in this test") {
		t.Fatalf("IsError %v text %q, want a tool error naming forja.verify and the reader's failure", res.IsError, text)
	}
}
