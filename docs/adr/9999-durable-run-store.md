# 9999 · Durable run store on Postgres

Status: proposed

## Context

The run store is in memory: a `forjad` restart loses every run, and
`forja.status` answers only for the process that ran the run (roadmap, M1;
ADR 0052, Consequences). M1 ends when a run survives a restart, and M2's
asynchronous runs and idempotency keys need a store that outlives the
process.

The in-memory store's observable behaviour is already the contract: its
tests pin filters, ordering, empty-not-nil lists and copies that share
nothing with the caller.

## Decision

1. **Driver.** `github.com/jackc/pgx/v5` with its pool. It is the
   maintained pure-Go driver; `database/sql` over it would add a layer and
   no feature the store uses.
2. **Selection.** `FORJA_DATABASE_URL` set: `serve` keeps runs in Postgres.
   Unset: in memory, as before. The URL comes from the environment only, and
   no error repeats it, since it carries the password.
3. **Schema.** One table, `runs`. The run is kept whole in a `json` column,
   encoded with `encoding/json`. Beside it, `repository`, `tool`, `state`
   and `started_at` are copies that let the query narrow a list. `json`,
   not `jsonb`, because `jsonb` refuses a NUL, and an agent can name a path
   with one. The columns only narrow the list: the filter itself decides,
   and the sort is `domain.CompareNewestFirst` in Go. That keeps the window
   exact to the nanosecond, where `timestamptz` keeps microseconds.
4. **Migrations.** Embedded SQL files, numbered, recorded in
   `forja_schema_migrations`. `forjad migrate` applies the pending ones in one
   transaction under an advisory lock: run twice, or twice at once, it
   applies each migration once. `serve` refuses a database whose schema
   version is not the one the binary knows, and says to run `forjad migrate`.
5. **Recovery.** On start, before releasing the claims a previous process
   journaled, `serve` closes the run of each one. A queued or running run
   ends `failed` with the new reason `interrupted`, and its open stages
   fail at the same instant, never before they started. The journal holds
   only the claims of a process that stopped (decision 7 makes that true),
   so this holds whether or not the ref is still the claim's. A run no journaled claim names is left
   alone, because another live `forjad` on the same database, such as a
   second stdio client, may be running it. Closing first means a store that
   fails stops the start with the journal intact.
   `serve` and `forjad migrate` both refuse a schema newer than the binary
   knows, with an error of its own (`ErrSchemaNewer`). The older binary's
   migrate cannot fix that, so rolling back after a migration needs the
   database restored too.
6. **Tests.** One behaviour suite runs against both stores. Postgres runs in
   a real container (`postgres:16-alpine`), gated like the Docker tests by
   `FORJA_DOCKER_TEST=1`, so it runs in `make docker-test` and in `make check`.
7. **One `serve` per state directory.** Recovery reads the journal as a
   dead process's, and with a durable store it now writes what it reads:
   two `serve` processes sharing `FORJA_STATE_DIR` would have the second
   close the first's live runs and delete its claim refs, so two runs would
   work one issue (CLAUDE.md rule 6). Before anything else, recovery
   included, `serve` takes an exclusive, non-blocking `flock` on
   `FORJA_STATE_DIR/forjad.lock` and holds it until it returns. A directory
   another process holds is a startup failure that names it, exit 1, with
   nothing recovered. The lock belongs to the open file description, so
   the kernel drops it when the process dies, however it dies: there is no
   PID file and nothing stale to clean. The descriptor is close-on-exec, so
   a child (`git`, the proxies) never keeps it. Without `FORJA_STATE_DIR`
   there is no journal and no lock. `forjad migrate` touches only the
   database and the proxy subcommands touch neither, so they do not take
   it; `forja.doctor` is a tool of `serve`, already under it.

## Consequences

- A run survives a restart, and `forja.status` reads it from any process
  pointed at the same database.
- The document is `domain.Run` encoded by its Go field names. Renaming or
  retyping a field is a data migration, not a refactor: old rows decode
  with the field empty. The round-trip test fills every field, so it catches
  a field the encoding drops, but it cannot see rows already written.
- `encoding/json` replaces invalid UTF-8 in a string with U+FFFD. A path
  that is not valid UTF-8 comes back changed. Byte streams (`[]byte`) are
  unaffected.
- `List` still loads every run the columns match and filters in Go, as the
  memory store does. Paging in SQL is left until the run history makes it
  matter.
- Metrics count `interrupted` under `unspecified`, the bucket for reasons
  outside their list, until the list grows.
- Some runs a crash leaves in flight stay `running`, because nothing
  records which process owns them: a `forja.doctor` run, which holds no
  claim; an implement run that died after its release removed the journal
  entry but before its last record; and every run when `FORJA_STATE_DIR`
  or the credentials are unset. Closing those needs the run to record its
  owner and a liveness lease. That is the same gap several replicas over
  one database hit (roadmap, open questions).
- The lock refuses a shared directory; it does not make several replicas
  cooperate. Each replica needs its own `FORJA_STATE_DIR`, and a `flock` on
  a network filesystem is only as good as that filesystem's lock support,
  so the directory belongs on local disk.
- The ledger table is `forja_schema_migrations`, so a database another tool
  migrates with its own `schema_migrations` does not collide.
- Idempotency keys and `caller_ref` (M2) get their own migration.
