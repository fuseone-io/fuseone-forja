package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/mcp"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
	"github.com/fuseone-io/fuseone-forja/internal/runstore/pgtest"
)

func dockerTests() bool { return os.Getenv("FORJA_DOCKER_TEST") == "1" }

// migratedRuns is the run store serve opens over dsn, once forjad migrate
// ran on it.
func migratedRuns(t *testing.T, dsn string) mcp.RunStore {
	t.Helper()
	env := envOf(map[string]string{EnvDatabaseURL: dsn})
	var stdout, stderr bytes.Buffer
	if code := migrate(context.Background(), &stdout, &stderr, env); code != 0 {
		t.Fatalf("migrate = %d: %s", code, stderr.String())
	}
	runs, closeRuns, err := openRuns(context.Background(), env)
	if err != nil {
		t.Fatalf("openRuns: %v", err)
	}
	t.Cleanup(closeRuns)
	return runs
}

func TestOpenRuns_databaseURLUnset_inMemoryStore(t *testing.T) {
	t.Parallel()
	runs, closeRuns, err := openRuns(context.Background(), emptyEnv)
	if err != nil {
		t.Fatalf("openRuns: %v", err)
	}
	defer closeRuns()
	if _, ok := runs.(*runstore.Memory); !ok {
		t.Fatalf("openRuns without %s = %T, want the in-memory store", EnvDatabaseURL, runs)
	}
}

// A database URL that does not parse is a startup failure that names the
// variable and not the URL, which carries the password.
func TestServe_malformedDatabaseURL_exitsOneWithoutTheURL(t *testing.T) {
	t.Parallel()
	const dsn = "postgres://forja:pw-not-on-stderr@[::1"
	env := func(k string) string {
		if k == EnvDatabaseURL {
			return dsn
		}
		return quietEnv(k)
	}
	// Bounded, so a server that starts anyway ends the test instead of
	// serving forever.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, serverTransport := sdk.NewInMemoryTransports()
	var stderr bytes.Buffer
	if code := serve(ctx, serverTransport, &stderr, env); code != 1 {
		t.Fatalf("serve = %d, want 1; stderr %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), EnvDatabaseURL) || strings.Contains(stderr.String(), "pw-not-on-stderr") {
		t.Fatalf("stderr %q: want it to name %s and not the password", stderr.String(), EnvDatabaseURL)
	}
}

func TestMigrateCommand_databaseURLUnset_exit2NamingTheVariable(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	if code := migrate(context.Background(), &stdout, &stderr, emptyEnv); code != 2 {
		t.Fatalf("migrate = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), EnvDatabaseURL) || stdout.Len() != 0 {
		t.Fatalf("stdout %q stderr %q: want nothing on stdout and the variable named on stderr", stdout.String(), stderr.String())
	}
}

// hostForjad builds this binary for the host, to run as a process of its
// own.
func hostForjad(ctx context.Context, t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "forjad")
	build := exec.CommandContext(ctx, "go", "build", "-o", path, "github.com/fuseone-io/fuseone-forja/cmd/forjad")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build forjad: %v\n%s", err, out)
	}
	return path
}

// runMigrate runs forjad migrate as a process of its own, with nothing in
// its environment but the database URL.
func runMigrate(ctx context.Context, t *testing.T, bin, dsn string) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, bin, "migrate")
	cmd.Env = []string{EnvDatabaseURL + "=" + dsn}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("forjad migrate: %v; stderr %q", err, stderr.String())
	}
	return stdout.String()
}

// forjad migrate is a process run against the database before serve: the
// first applies the schema, the second finds nothing to do and succeeds.
func TestMigrateCommand_twice_secondAppliesNothing(t *testing.T) {
	t.Parallel()
	srv := pgtest.Start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	bin, dsn := hostForjad(ctx, t), srv.Database(t)

	if first := runMigrate(ctx, t, bin, dsn); !strings.Contains(first, "applied migration 1") {
		t.Fatalf("first migrate stdout %q, want it to report migration 1 applied", first)
	}
	if second := runMigrate(ctx, t, bin, dsn); !strings.Contains(second, "schema is current") || strings.Contains(second, "applied") {
		t.Fatalf("second migrate stdout %q, want it to report the schema current and nothing applied", second)
	}
}

// statusOf asks forja.status for one run through a server started on env.
func statusOf(t *testing.T, env func(string) string, id string) map[string]json.RawMessage {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clientTransport, serverTransport := sdk.NewInMemoryTransports()
	var stderr bytes.Buffer
	exited := make(chan int, 1)
	go func() { exited <- serve(ctx, serverTransport, &stderr, env) }()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "restart-test", Version: "test"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "forja.status", Arguments: map[string]any{"run_id": id}})
	if err != nil || res.IsError {
		t.Fatalf("forja.status = %+v, %v", res, err)
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("status output %s: %v", raw, err)
	}
	_ = cs.Close()
	cancel()
	if code := <-exited; code != 0 {
		t.Fatalf("serve = %d: %s", code, stderr.String())
	}
	return wire
}

// A run one process recorded is what forja.status answers in the next
// one: the writer's pool is closed before serve opens its own.
func TestServe_postgres_runRecordedBeforeRestartReadByStatus(t *testing.T) {
	t.Parallel()
	srv := pgtest.Start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dsn := srv.Database(t)
	runMigrate(ctx, t, hostForjad(ctx, t), dsn)
	env := func(k string) string {
		if k == EnvDatabaseURL {
			return dsn
		}
		return quietEnv(k)
	}
	finished := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	run := domain.Run{
		ID: "run-before-restart", Tool: domain.ToolImplement, Repository: "acme/api", Issue: 7, Client: "fuseone-pilot",
		State: domain.StateSucceeded, StartedAt: finished.Add(-time.Hour), FinishedAt: &finished,
	}
	writer, closeWriter, err := openRuns(ctx, env)
	if err != nil {
		t.Fatalf("openRuns: %v", err)
	}
	if err := writer.Put(ctx, run); err != nil {
		t.Fatalf("Put: %v", err)
	}
	closeWriter()

	wire := statusOf(t, env, run.ID)
	if string(wire["id"]) != `"run-before-restart"` || string(wire["state"]) != `"succeeded"` || string(wire["finished_at"]) != `"2026-11-05T10:00:00Z"` {
		t.Fatalf("forja.status after restart = id %s state %s finished_at %s, want the run recorded before", wire["id"], wire["state"], wire["finished_at"])
	}
}
