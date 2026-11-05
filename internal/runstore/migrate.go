package runstore

import (
	"cmp"
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
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

// Migrations is every schema change this binary knows, oldest first.
func Migrations() []Migration {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		panic(fmt.Sprintf("runstore: embedded migrations: %v", err)) // the directory is compiled in
	}
	out := make([]Migration, 0, len(entries))
	for _, e := range entries {
		version, err := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if err != nil {
			panic(fmt.Sprintf("runstore: migration %s has no version prefix", e.Name())) // the names are compiled in
		}
		body, err := migrationFiles.ReadFile(path.Join("migrations", e.Name()))
		if err != nil {
			panic(fmt.Sprintf("runstore: embedded migration %s: %v", e.Name(), err)) // the files are compiled in
		}
		out = append(out, Migration{Version: version, SQL: string(body)})
	}
	slices.SortFunc(out, func(a, b Migration) int { return cmp.Compare(a.Version, b.Version) })
	return out
}

func latestVersion() int {
	all := Migrations()
	return all[len(all)-1].Version
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
	const ledger = `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    integer     PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now())`
	if _, err := tx.Exec(ctx, ledger); err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	current, err := schemaVersion(ctx, tx)
	if err != nil {
		return nil, err
	}
	applied := []int{}
	for _, m := range Migrations() {
		if m.Version <= current {
			continue
		}
		if _, err := tx.Exec(ctx, m.SQL); err != nil {
			return nil, fmt.Errorf("migration %d: %w", m.Version, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", m.Version); err != nil {
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
	if err := q.QueryRow(ctx, "SELECT to_regclass('schema_migrations') IS NOT NULL").Scan(&exists); err != nil {
		return 0, fmt.Errorf("schema version: %w", err)
	}
	if !exists {
		return 0, nil
	}
	var version int
	if err := q.QueryRow(ctx, "SELECT coalesce(max(version), 0) FROM schema_migrations").Scan(&version); err != nil {
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
