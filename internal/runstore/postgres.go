package runstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// errNotOpen is what every call on a Postgres that OpenPostgres did not
// return fails with.
var errNotOpen = errors.New("run store: Postgres is not open")

// Postgres is the run store kept in a Postgres database, so that a run
// outlives the process that ran it. Each run is kept whole as a json
// document, so that what Get returns is what Put was given.
type Postgres struct {
	pool *pgxpool.Pool
}

// OpenPostgres connects to the database at dsn and checks that its schema
// is the one this binary knows; a database forjad migrate has not brought
// up to date is refused with ErrSchemaNotMigrated.
func OpenPostgres(ctx context.Context, dsn string) (*Postgres, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("open run store: database URL does not parse")
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open run store: %w", err)
	}
	version, err := schemaVersion(ctx, pool)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("open run store: %w", err)
	}
	if err := checkVersion(version, latestVersion()); err != nil {
		pool.Close()
		return nil, fmt.Errorf("open run store: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

// Close releases the store's connections. Closing a store never opened
// does nothing.
func (p *Postgres) Close() {
	if p.pool != nil {
		p.pool.Close()
	}
}

// Put stores the run under its ID, replacing any run already there. An
// invalid run is rejected and leaves the store untouched.
func (p *Postgres) Put(ctx context.Context, run domain.Run) error {
	if err := run.Validate(); err != nil {
		return fmt.Errorf("put run: %w", err)
	}
	if p.pool == nil {
		return fmt.Errorf("put run: %w", errNotOpen)
	}
	doc, err := json.Marshal(run)
	if err != nil {
		return fmt.Errorf("put run %q: %w", run.ID, err)
	}
	const upsert = `INSERT INTO runs (id, repository, tool, state, started_at, run)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (id) DO UPDATE SET repository = EXCLUDED.repository, tool = EXCLUDED.tool,
			state = EXCLUDED.state, started_at = EXCLUDED.started_at, run = EXCLUDED.run`
	_, err = p.pool.Exec(ctx, upsert, run.ID, run.Repository, string(run.Tool), string(run.State), run.StartedAt, string(doc))
	if err != nil {
		return fmt.Errorf("put run %q: %w", run.ID, err)
	}
	return nil
}

// Get returns the run with the given ID, or an error wrapping
// domain.ErrRunNotFound.
func (p *Postgres) Get(ctx context.Context, id string) (domain.Run, error) {
	if p.pool == nil {
		return domain.Run{}, fmt.Errorf("run %q: %w", id, errNotOpen)
	}
	// Put refuses such an id, so no run has it; the column could not even
	// be compared with it.
	if !domain.IsText(id) {
		return domain.Run{}, fmt.Errorf("run %q: %w", id, domain.ErrRunNotFound)
	}
	var doc []byte
	err := p.pool.QueryRow(ctx, "SELECT run FROM runs WHERE id = $1", id).Scan(&doc)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Run{}, fmt.Errorf("run %q: %w", id, domain.ErrRunNotFound)
	}
	if err != nil {
		return domain.Run{}, fmt.Errorf("run %q: %w", id, err)
	}
	return decode(id, doc)
}

// List returns every run the filter matches, sorted with
// domain.CompareNewestFirst; empty, never nil, when none does. An invalid
// filter is an error. The query narrows by the columns; the filter itself
// decides, to the nanosecond the columns do not keep.
func (p *Postgres) List(ctx context.Context, f domain.RunFilter) ([]domain.Run, error) {
	if err := f.Validate(); err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	if p.pool == nil {
		return nil, fmt.Errorf("list runs: %w", errNotOpen)
	}
	const query = `SELECT id, run FROM runs
		WHERE ($1 = '' OR repository = $1) AND ($2 = '' OR tool = $2) AND ($3 = '' OR state = $3)
			AND ($4::timestamptz IS NULL OR started_at >= $4) AND ($5::timestamptz IS NULL OR started_at <= $5)`
	rows, err := p.pool.Query(ctx, query, f.Repository, string(f.Tool), string(f.State), bound(f.Since), bound(f.Until))
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	out, err := collect(rows, f)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	slices.SortFunc(out, domain.CompareNewestFirst)
	return out, nil
}

// bound is a window end as the column compares it: microseconds, rounded
// down as the column rounds a start down, so the query never drops a run
// the filter keeps. Unset is NULL.
func bound(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	b := t.Truncate(time.Microsecond)
	return &b
}

func collect(rows pgx.Rows, f domain.RunFilter) ([]domain.Run, error) {
	defer rows.Close()
	out := make([]domain.Run, 0)
	for rows.Next() {
		var id string
		var doc []byte
		if err := rows.Scan(&id, &doc); err != nil {
			return nil, err
		}
		run, err := decode(id, doc)
		if err != nil {
			return nil, err
		}
		if f.Match(run) {
			out = append(out, run)
		}
	}
	return out, rows.Err()
}

func decode(id string, doc []byte) (domain.Run, error) {
	var run domain.Run
	if err := json.Unmarshal(doc, &run); err != nil {
		return domain.Run{}, fmt.Errorf("run %q: decode: %w", id, err)
	}
	return run, nil
}
