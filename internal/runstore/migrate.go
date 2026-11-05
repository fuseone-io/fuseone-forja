package runstore

import (
	"cmp"
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migrationLock is the advisory lock key every migrator takes, so that two
// started together apply each migration once.
const migrationLock int64 = 0x666f726a61 // "forja"

// ErrSchemaNotMigrated means the database's schema is not the one this
// binary reads and writes.
var ErrSchemaNotMigrated = errors.New("run store schema is not migrated: run forjad migrate")

// Migration is one schema change, numbered in the order it applies.
type Migration struct {
	Version int
	SQL     string
}

// embedded is every migration compiled into the binary, read once at
// init and never written after.
var embedded []Migration

// A migration set that does not load is a broken build: it fails every
// start of every subcommand, and every test of this package, before any
// database is touched.
func init() {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err == nil {
		embedded, err = loadMigrations(sub)
	}
	if err != nil {
		panic(fmt.Sprintf("runstore: embedded migrations: %v", err))
	}
}

// Migrations is every schema change this binary knows, oldest first.
func Migrations() []Migration {
	return slices.Clone(embedded)
}

// loadMigrations reads every <version>_<name>.sql at the root of fsys,
// oldest first. A file whose name has no version, two files with one
// version, or an empty set is an error.
func loadMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	out := make([]Migration, 0, len(entries))
	for _, e := range entries {
		prefix, _, found := strings.Cut(e.Name(), "_")
		version, err := strconv.Atoi(prefix)
		if !found || err != nil || version <= 0 {
			return nil, fmt.Errorf("migration %s has no <version>_ prefix", e.Name())
		}
		body, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("migration %s: %w", e.Name(), err)
		}
		out = append(out, Migration{Version: version, SQL: string(body)})
	}
	slices.SortFunc(out, func(a, b Migration) int { return cmp.Compare(a.Version, b.Version) })
	return out, checkSequence(out)
}

// checkSequence refuses an empty set and a version used twice.
func checkSequence(ms []Migration) error {
	if len(ms) == 0 {
		return errors.New("no migration")
	}
	for i := 1; i < len(ms); i++ {
		if ms[i].Version == ms[i-1].Version {
			return fmt.Errorf("migration version %d is used twice", ms[i].Version)
		}
	}
	return nil
}

// ErrSchemaNewer means the database was migrated by a newer binary than
// this one, which cannot know what the newer schema changed.
var ErrSchemaNewer = errors.New("run store schema is newer than this binary: run the binary that migrated it")

// checkVersion accepts only the schema this binary knows: an older one
// needs forjad migrate, a newer one a newer binary.
func checkVersion(version, want int) error {
	switch {
	case version < want:
		return fmt.Errorf("schema version %d, this binary needs %d: %w", version, want, ErrSchemaNotMigrated)
	case version > want:
		return fmt.Errorf("schema version %d, this binary knows up to %d: %w", version, want, ErrSchemaNewer)
	}
	return nil
}

func latestVersion() int {
	return embedded[len(embedded)-1].Version
}

// Migrate applies, in one transaction, every migration the database has
// not had yet, and returns the versions it applied: none when the schema
// was already current.
func Migrate(ctx context.Context, dsn string) ([]int, error) {
	conn, err := connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	var applied []int
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		applied, err = applyPending(ctx, tx)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return applied, nil
}

func applyPending(ctx context.Context, tx pgx.Tx) ([]int, error) {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLock); err != nil {
		return nil, fmt.Errorf("lock: %w", err)
	}
	const ledger = `CREATE TABLE IF NOT EXISTS forja_schema_migrations (
		version    integer     PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now())`
	if _, err := tx.Exec(ctx, ledger); err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	current, err := schemaVersion(ctx, tx)
	if err != nil {
		return nil, err
	}
	if latest := latestVersion(); current > latest {
		return nil, checkVersion(current, latest)
	}
	applied := []int{}
	for _, m := range embedded {
		if m.Version <= current {
			continue
		}
		if _, err := tx.Exec(ctx, m.SQL); err != nil {
			return nil, fmt.Errorf("migration %d: %w", m.Version, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO forja_schema_migrations (version) VALUES ($1)", m.Version); err != nil {
			return nil, fmt.Errorf("record migration %d: %w", m.Version, err)
		}
		applied = append(applied, m.Version)
	}
	return applied, nil
}

// querier is a connection, a pool or a transaction; the arguments are
// any because that is how pgx takes query parameters.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// schemaVersion is the newest migration the database has had; zero when
// it has had none.
func schemaVersion(ctx context.Context, q querier) (int, error) {
	var exists bool
	if err := q.QueryRow(ctx, "SELECT to_regclass('forja_schema_migrations') IS NOT NULL").Scan(&exists); err != nil {
		return 0, fmt.Errorf("schema version: %w", err)
	}
	if !exists {
		return 0, nil
	}
	var version int
	if err := q.QueryRow(ctx, "SELECT coalesce(max(version), 0) FROM forja_schema_migrations").Scan(&version); err != nil {
		return 0, fmt.Errorf("schema version: %w", err)
	}
	return version, nil
}

// connect opens one connection. A URL that does not parse is reported
// without its text, which carries the password.
func connect(ctx context.Context, dsn string) (*pgx.Conn, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("database URL does not parse")
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	return conn, nil
}
