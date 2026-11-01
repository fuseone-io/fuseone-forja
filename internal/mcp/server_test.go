package mcp_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	forja "github.com/fuseone-io/fuseone-forja/internal/mcp"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

// connect wires New(store, "test") to a client over in-memory transports and
// closes both ends when the test ends.
func connect(t *testing.T, store *runstore.Memory) *sdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	server := forja.New(store, "test")
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

func TestStatus_missingRunID_rejectedByInputSchema(t *testing.T) {
	t.Parallel()
	store := runstore.NewMemory()
	mustPut(t, store, runningRun("run-present"))
	cs := connect(t, store)

	res := callStatus(t, cs, map[string]any{})
	if !res.IsError {
		t.Fatalf("IsError = false for a call without run_id; content: %s", textOf(res))
	}
	text := textOf(res)
	if !strings.Contains(text, "run_id") {
		t.Errorf("error text %q does not name run_id", text)
	}
	if strings.Contains(text, "not found") {
		t.Errorf("error text %q comes from the store, not from the input schema", text)
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
	want := []string{"id", "tool", "repository", "issue", "state", "started_at", "finished_at"}
	for _, key := range want {
		if _, ok := wire[key]; !ok {
			t.Errorf("key %q absent on the wire", key)
		}
	}
	if len(wire) != len(want) {
		t.Errorf("wire object has %d keys, want %d: %s", len(wire), len(want), structuredJSON(t, res))
	}
}

func TestTools_notImplemented_toolErrorNotProtocolError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		tool domain.Tool
		args map[string]any
	}{
		{tool: domain.ToolDoctor, args: map[string]any{"repository": "fuseone-io/fuseone-forja"}},
		{tool: domain.ToolPlan, args: map[string]any{"repository": "fuseone-io/fuseone-forja", "issue": 5}},
		{tool: domain.ToolImplement, args: map[string]any{
			"repository": "fuseone-io/fuseone-forja", "issue": 5, "base_branch": "main",
		}},
		{tool: domain.ToolVerify, args: map[string]any{"repository": "fuseone-io/fuseone-forja", "pull_request": 9}},
	}
	for _, tt := range tests {
		t.Run(string(tt.tool), func(t *testing.T) {
			t.Parallel()
			cs := connect(t, runstore.NewMemory())

			res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{
				Name:      string(tt.tool),
				Arguments: tt.args,
			})
			if err != nil {
				t.Fatalf("CallTool(%s): protocol error %v, want a tool error", tt.tool, err)
			}
			if !res.IsError {
				t.Fatalf("CallTool(%s): IsError = false; content: %s", tt.tool, textOf(res))
			}
			text := textOf(res)
			if !strings.Contains(text, "not implemented") {
				t.Errorf("%s: error text %q does not say not implemented", tt.tool, text)
			}
			if !strings.Contains(text, string(tt.tool)) {
				t.Errorf("%s: error text %q does not name the tool", tt.tool, text)
			}
		})
	}
}
