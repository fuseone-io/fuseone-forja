package runstore_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
	"github.com/fuseone-io/fuseone-forja/internal/runstore/pgtest"
)

func ctxFor(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func migrate(t *testing.T, dsn string) []int {
	t.Helper()
	applied, err := runstore.Migrate(ctxFor(t), dsn)
	if err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return applied
}

func openPostgres(t *testing.T, dsn string) *runstore.Postgres {
	t.Helper()
	p, err := runstore.OpenPostgres(ctxFor(t), dsn)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// migratedStore is a store over a database of its own, migrated.
func migratedStore(t *testing.T, srv *pgtest.Server) *runstore.Postgres {
	t.Helper()
	dsn := srv.Database(t)
	migrate(t, dsn)
	return openPostgres(t, dsn)
}

// One container serves every scenario; each scenario has a database of its
// own.
func TestPostgres_realContainer(t *testing.T) {
	t.Parallel()
	srv := pgtest.Start(t)
	t.Run("behaviourSuite", func(t *testing.T) {
		t.Parallel()
		runSuite(t, func(t *testing.T) store { return migratedStore(t, srv) })
	})
	for _, s := range postgresScenarios {
		t.Run(s.name, func(t *testing.T) {
			t.Parallel()
			s.test(t, srv)
		})
	}
}

var postgresScenarios = []struct {
	name string
	test func(t *testing.T, srv *pgtest.Server)
}{
	{"reopened_runWrittenBeforeIsRead", testPostgres_reopened_runWrittenBeforeIsRead},
	{"migrateTwice_secondAppliesNothingAndKeepsRuns", testPostgres_migrateTwice_secondAppliesNothingAndKeepsRuns},
	{"concurrentMigrate_allSucceedSchemaOnce", testPostgres_concurrentMigrate_allSucceedSchemaOnce},
	{"unmigrated_openRefusedNamingMigrate", testPostgres_unmigrated_openRefusedNamingMigrate},
	{"newerSchema_openAndMigrateRefused", testPostgres_newerSchema_openAndMigrateRefused},
	{"unreachable_errorWithholdsPassword", testPostgres_unreachable_errorWithholdsPassword},
	{"sharedDatabase_otherAppsRunsTableUntouched", testPostgres_sharedDatabase_otherAppsRunsTableUntouched},
}

// A database another application also uses may already have a table
// named runs: migrate leaves it alone, and the store neither reads nor
// writes it.
func testPostgres_sharedDatabase_otherAppsRunsTableUntouched(t *testing.T, srv *pgtest.Server) {
	dsn := srv.Database(t)
	conn, err := pgx.Connect(ctxFor(t), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if _, err := conn.Exec(ctxFor(t), "CREATE TABLE runs (id text PRIMARY KEY, owner text NOT NULL)"); err != nil {
		t.Fatalf("the other application's table: %v", err)
	}
	if _, err := conn.Exec(ctxFor(t), "INSERT INTO runs (id, owner) VALUES ('run-1', 'billing')"); err != nil {
		t.Fatalf("the other application's row: %v", err)
	}

	migrate(t, dsn)
	store := openPostgres(t, dsn)
	if err := store.Put(ctxFor(t), sampleRun("run-1")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	var owner string
	if err := conn.QueryRow(ctxFor(t), "SELECT owner FROM runs WHERE id = 'run-1'").Scan(&owner); err != nil || owner != "billing" {
		t.Fatalf("the other application's row = %q (%v), want it untouched", owner, err)
	}
}

// A run one pool wrote is what a pool opened after the first closed reads:
// the store outlives the process that wrote it.
func testPostgres_reopened_runWrittenBeforeIsRead(t *testing.T, srv *pgtest.Server) {
	dsn := srv.Database(t)
	migrate(t, dsn)
	want := stagedRun()
	first, err := runstore.OpenPostgres(ctxFor(t), dsn)
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	if err := first.Put(ctxFor(t), want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	first.Close()

	got, err := openPostgres(t, dsn).Get(ctxFor(t), want.ID)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Get after reopening = %+v (%v), want %+v", got, err, want)
	}
}

func testPostgres_migrateTwice_secondAppliesNothingAndKeepsRuns(t *testing.T, srv *pgtest.Server) {
	dsn := srv.Database(t)
	if first := migrate(t, dsn); len(first) == 0 {
		t.Fatal("first Migrate applied nothing on an empty database")
	}
	store := openPostgres(t, dsn)
	if err := store.Put(ctxFor(t), sampleRun("run-1")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if second := migrate(t, dsn); len(second) != 0 {
		t.Fatalf("second Migrate applied %v, want nothing", second)
	}
	if _, err := store.Get(ctxFor(t), "run-1"); err != nil {
		t.Fatalf("Get after the second Migrate: %v", err)
	}
	if again := openPostgres(t, dsn); again == nil {
		t.Fatal("OpenPostgres after the second Migrate returned no store")
	}
}

// Two forjad migrate started together leave one schema: exactly one of
// them applies each migration and neither fails.
func testPostgres_concurrentMigrate_allSucceedSchemaOnce(t *testing.T, srv *pgtest.Server) {
	dsn := srv.Database(t)
	const migrators = 4
	results := make([][]int, migrators)
	errs := make([]error, migrators)
	var wg sync.WaitGroup
	for i := range migrators {
		wg.Go(func() { results[i], errs[i] = runstore.Migrate(ctxFor(t), dsn) })
	}
	wg.Wait()

	var applied []int
	for i := range migrators {
		if errs[i] != nil {
			t.Fatalf("Migrate %d: %v", i, errs[i])
		}
		applied = append(applied, results[i]...)
	}
	if want := migrate(t, dsn); len(want) != 0 {
		t.Fatalf("Migrate after the concurrent ones applied %v, want nothing", want)
	}
	if len(applied) != len(runstore.Migrations()) {
		t.Fatalf("concurrent Migrate applied %v in all, want each of %d migrations once", applied, len(runstore.Migrations()))
	}
}

func testPostgres_unmigrated_openRefusedNamingMigrate(t *testing.T, srv *pgtest.Server) {
	dsn := srv.Database(t)
	p, err := runstore.OpenPostgres(ctxFor(t), dsn)
	if err == nil {
		p.Close()
		t.Fatal("OpenPostgres accepted a database with no schema")
	}
	if !errors.Is(err, runstore.ErrSchemaNotMigrated) || !strings.Contains(err.Error(), "forjad migrate") {
		t.Fatalf("OpenPostgres error = %v, want ErrSchemaNotMigrated naming forjad migrate", err)
	}
}

// A database a newer binary migrated is refused, by open and by migrate,
// with its own error: running
// forjad migrate from this binary cannot help, so the error does not say to.
func testPostgres_newerSchema_openAndMigrateRefused(t *testing.T, srv *pgtest.Server) {
	dsn := srv.Database(t)
	migrate(t, dsn)
	conn, err := pgx.Connect(ctxFor(t), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if _, err := conn.Exec(ctxFor(t), "INSERT INTO forja_schema_migrations (version) VALUES (9999)"); err != nil {
		t.Fatalf("record a newer migration: %v", err)
	}

	p, err := runstore.OpenPostgres(ctxFor(t), dsn)
	if err == nil {
		p.Close()
		t.Fatal("OpenPostgres accepted a schema newer than the binary")
	}
	if !errors.Is(err, runstore.ErrSchemaNewer) || errors.Is(err, runstore.ErrSchemaNotMigrated) || strings.Contains(err.Error(), "forjad migrate") {
		t.Fatalf("OpenPostgres error = %v, want ErrSchemaNewer without the advice to migrate", err)
	}
	if applied, err := runstore.Migrate(ctxFor(t), dsn); !errors.Is(err, runstore.ErrSchemaNewer) {
		t.Fatalf("Migrate on a newer schema = %v, %v; want ErrSchemaNewer, not a schema reported current", applied, err)
	}
}

// The URL carries the database password; neither the open nor the
// migration repeats it in an error, which ends up on stderr.
func testPostgres_unreachable_errorWithholdsPassword(t *testing.T, _ *pgtest.Server) {
	const password = "pw-must-not-leak-9f2c"
	dsn := "postgres://forja:" + password + "@127.0.0.1:1/forja?sslmode=disable&connect_timeout=2"
	p, err := runstore.OpenPostgres(ctxFor(t), dsn)
	if err == nil {
		p.Close()
		t.Fatal("OpenPostgres reached a server on port 1")
	}
	_, merr := runstore.Migrate(ctxFor(t), dsn)
	for _, e := range []error{err, merr} {
		if e == nil || strings.Contains(e.Error(), password) {
			t.Fatalf("error %v: want an error without the password", e)
		}
	}
	_, perr := runstore.OpenPostgres(ctxFor(t), "postgres://forja:"+password+"@[::1")
	if perr == nil || strings.Contains(perr.Error(), password) {
		t.Fatalf("malformed URL error %v: want an error without the password", perr)
	}
}

// A Postgres that was never opened fails every call loudly instead of
// dereferencing nothing.
func TestPostgres_zeroValue_everyCallFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var p runstore.Postgres
	if err := p.Put(ctx, sampleRun("run-1")); err == nil {
		t.Fatal("Put on the zero value succeeded")
	}
	if _, err := p.Get(ctx, "run-1"); err == nil || errors.Is(err, domain.ErrRunNotFound) {
		t.Fatalf("Get on the zero value = %v, want an error other than not found", err)
	}
	if _, err := p.List(ctx, domain.RunFilter{}); err == nil {
		t.Fatal("List on the zero value succeeded")
	}
	p.Close()
}
