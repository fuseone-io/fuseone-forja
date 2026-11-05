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
   `schema_migrations`. `forjad migrate` applies the pending ones in one
   transaction under an advisory lock: run twice, or twice at once, it
   applies each migration once. `serve` refuses a database whose schema
   version is not the one the binary knows, and says to run `forjad migrate`.
5. **Recovery.** On start, before releasing the claims a previous process
   journaled, `serve` closes every queued or running run in the store. Each
   one ends `failed` with the new reason `interrupted`, and its open stages
   fail at the same instant. With one replica and nothing run yet, every
   run in flight belongs to a process that died. That covers runs no claim
   names: a `forja.doctor` run, a run that died between its release and its
   last record, and every run when `FORJA_STATE_DIR` or the credentials are
   unset. Closing first means a store that fails stops the start with the
   journal intact.
   `serve` also refuses a schema newer than the binary knows, with an error
   of its own (`ErrSchemaNewer`): `forjad migrate` cannot fix that, so
   rolling back after a migration needs the database restored too.
6. **Tests.** One behaviour suite runs against both stores. Postgres runs in
   a real container (`postgres:16-alpine`), gated like the Docker tests by
   `FORJA_DOCKER_TEST=1`, so it runs in `make docker-test` and in `make check`.

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
- One replica still. Recovery closes every run in flight, so a second
  replica starting would close the first one's runs. Several replicas over
  one database need runs that record which replica holds them (roadmap,
  open questions).
- Idempotency keys and `caller_ref` (M2) get their own migration.
