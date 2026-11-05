package main

import (
	"context"
	"fmt"
	"io"

	"github.com/fuseone-io/fuseone-forja/internal/mcp"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

// EnvDatabaseURL is the Postgres URL the run store is kept in. Unset, runs
// are kept in memory and a restart loses them.
const EnvDatabaseURL = "FORJA_DATABASE_URL"

// openRuns is the run store serve records runs in: Postgres at
// FORJA_DATABASE_URL, migrated, or memory when the variable is unset. The
// returned func releases the store.
func openRuns(ctx context.Context, getenv func(string) string) (mcp.RunStore, func(), error) {
	dsn := getenv(EnvDatabaseURL)
	if dsn == "" {
		return runstore.NewMemory(), func() {}, nil
	}
	p, err := runstore.OpenPostgres(ctx, dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", EnvDatabaseURL, err)
	}
	return p, p.Close, nil
}

// migrate brings the schema of the database at FORJA_DATABASE_URL up to
// the one this binary reads and writes, and says on stdout what it
// applied. Run again it applies nothing and succeeds.
func migrate(ctx context.Context, stdout, stderr io.Writer, getenv func(string) string) int {
	dsn := getenv(EnvDatabaseURL)
	if dsn == "" {
		// The exit code is the only channel left when stderr itself fails.
		_, _ = fmt.Fprintf(stderr, "forjad: migrate: %s is not set\n", EnvDatabaseURL)
		return 2
	}
	applied, err := runstore.Migrate(ctx, dsn)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "forjad: migrate: %v\n", err)
		return 1
	}
	if len(applied) == 0 {
		_, _ = fmt.Fprintln(stdout, "forjad: migrate: schema is current")
	}
	for _, v := range applied {
		_, _ = fmt.Fprintf(stdout, "forjad: migrate: applied migration %d\n", v)
	}
	return 0
}
