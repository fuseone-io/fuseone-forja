# ADR 9999: asynchronous runs, forja.cancel and per-client idempotency

- **Status:** proposed
- **Issue:** #63
- **Refines:** [ADR 0052](0052-network-transport-and-caller-authentication.md)
  §6, §7 and Consequences, which leave runs that outlive the request, a
  `forja.cancel` and an idempotency key to their own ADR, and
  [ADR 0076](0076-durable-run-store.md), Consequences, which leaves
  idempotency keys and `caller_ref` to their own migration. This is that
  ADR. It supersedes nothing.
- **Date:** 2026-11-06

## Context

The roadmap's M2 (`docs/roadmap.md`, "M2 · Long runs as a contract") changes
the tool schemas. ADR 0051 §1 freezes them under semver from v1.0.0, so these
changes land before the first tagged major or they cost a v2. This ADR fixes
the shape of all of them at once, so the slices that implement M2 change the
schemas once.

What the code does today (`origin/main` at `1e482a2`):

- **A run lasts as long as its call.** `forja.implement` and `forja.verify`
  return when the run has ended (`internal/mcp/implement.go`, `implement`;
  `internal/mcp/verify.go`, `verify`). The call reads the contract to size the
  lease (`internal/mcp/lifecycle.go`, `lease`), takes the claim on the
  request's context (`lifecycle.claim`), writes the `running` record and runs
  every stage under that same context (`lifecycle.run`). Only the records and
  the release run on a detached context (`context.WithoutCancel`, each bounded
  by `FinishTimeout`, 30 s).
- **A dropped caller cancels the run.** Over HTTP that holds only because the
  server accepts protocol 2026-07-28 alone and propagates the request's
  cancellation (`cmd/forjad/mcp_http.go`, `PropagateRequestCancellation:
  true`; `cmd/forjad/mcp_protocol.go`, `SupportedProtocolVersions`; ADR 0052
  §7). Once the work's context ends, no stage starts
  (`internal/mcp/implement_stages.go`, `flight.stage`), so the teardown is
  bounded: `claim.SettleBound` plus `mcp.CancelledRunRecords` ×
  `FinishTimeout`, 180 s, and the drain is that plus 15 s
  (`cmd/forjad/mcp_http.go`, `teardownBound`, `mcpShutdownTimeout`).
- **`forja.doctor` blocks too.** It runs the contract's setup and gates in a
  workspace under the contract's wall clock (`internal/mcp/doctor.go`,
  `runGates`), records a run, and holds no claim.
- **Nothing limits how many runs a process starts.** There is no admission
  check anywhere in `internal/mcp` or `cmd/forjad`; the state `queued` exists
  (`internal/domain/run.go`) but no run is ever recorded in it.
- **The ceilings are the contract's and the operator's.** The work's deadline
  is `limits.wall_clock` counted from the claim (`lifecycle.timeLeft`, then
  `context.WithTimeoutCause(ctx, f.left, errWallClock)` in
  `internal/mcp/implement_agent.go` and `internal/mcp/verify_stages.go`). The
  model's ceiling is one process-wide number, `FORJA_MODEL_TOKEN_CAP`
  (`cmd/forjad/agent.go`), enforced by the run's model proxy. There is no cost
  ceiling: the estimated cost is computed when a run is answered, from the
  operator's price table, and is absent without one (`internal/mcp/cost.go`).
- **The run store knows the client and nothing else of the caller.** Runs are
  one JSON document per row, with `repository`, `tool`, `state`, `started_at`
  and `client` beside it (`internal/runstore/migrations/0001_runs.sql`,
  `0002_run_client.sql`). `GetOwned` reads a run only under its own client
  (`internal/runstore/postgres.go`, `internal/runstore/memory.go`), and
  `forja.status` uses it so that another client's run is the same error as an
  id that never existed (`internal/mcp/status_owned.go`).
- **`forja.status` mixes what the control plane measured with what the agent
  influenced.** One flat record carries the state, exit codes, SHAs and cost
  beside the gate logs (`stdout`, `stderr`), the paths the agent's head
  changed (`proof_apparatus`, `human_merge_paths`), the destinations the
  workspace tried to reach (`egress_denied`) and the registry paths it asked
  for (`registry_access`) (`internal/mcp/status.go`, `StatusOutput`). The
  outputs of `forja.implement` and `forja.verify` add the rendered evidence,
  whose body is also the pull request's (`internal/mcp/implement_deliver.go`,
  `pullRequestStage`).
- **Recovery closes only journaled runs.** On start, `serve` closes the run of
  every claim a dead process journaled as `failed` with reason `interrupted`,
  then releases those claims (`cmd/forjad/recover.go`; ADR 0076 §5). A run
  that no journaled claim names stays as it was recorded.

What the callers need (PRD, "Alignment with the FuseOne factory (NT-013)",
items 2 to 5; roadmap, "How FuseOne reaches an ephemeral Forja", items 4 to
6):

- A run lasts up to `limits.wall_clock`, 30 minutes by default
  (`domain.DefaultWallClock`), longer than a tool call should stay open, and
  FuseOne's Developer starts a run in one pass and reads its outcome in a
  later one.
- A retry after an unknown outcome (a timeout, a dropped connection, a worker
  restart) must not start a second run, but a correction round issued from the
  same FuseOne run must.
- FuseOne joins Forja's cost to its card, caps the runtime from the
  platform's per-run budget, and cancels Forja's run when it cancels its own.
- A role that must stay untainted reads only fields the control plane
  produced.

## Decision

### 1. `implement` and `verify` return the `run_id` when the run has started

`forja.implement` and `forja.verify` do, inside the call, everything that can
refuse the operation, and nothing after:

1. Validate the input's shape: types, lengths and character sets (§3, §4).
   Nothing here depends on the clock.
2. Look up the idempotency key (§3). A replay answers here, whatever the
   clock, the capacity or the claim now say.
3. Refuse a `deadline` already passed (§4).
4. Admission: take a slot (§6).
5. What each tool reads before its claim today: the correction round's pull
   request (`openRound`, `internal/mcp/implement_round.go`) or the verified
   pull request (`verify`), then the contract at the base commit to size the
   lease (`lifecycle.lease`).
6. Apply the caller's deadline and budget (§4), and refuse again with
   `deadline_passed` a deadline that passed during step 5, so no claim is
   taken for a run that could not work.
7. Take the claim.
8. Register the run with the **run supervisor**, which creates the run's
   context and keeps its cancel function. A supervisor that is stopping
   refuses: the call releases its claim, gives its slot back and answers a
   tool error, with no run and no key.
9. In one store transaction, record the run as `running` and the idempotency
   key (§3).
10. Start the run's goroutine and answer `RunStarted` (schemas below).

Any refusal in steps 1 to 8 is a tool error, records no run, stores no key and
leaves no claim, as a refused claim does today; a refusal after step 4 gives
its slot back before it answers. Once the claim is held (step 7), steps 8 to
10 run on a context detached from the request's, each bounded by
`FinishTimeout`: from that point the run exists whether or not the caller is
still there, and a caller that went away finds it again with the same
idempotency key (§3). A step 9 that fails unregisters the run, releases the
claim on that detached context, gives the slot back and is the tool error, as
`lifecycle.run` does with a first record that fails today.

The **run supervisor** is a struct of `internal/mcp` that `serve` builds and
owns. Its root context is its own: `context.WithCancelCause` over
`context.WithoutCancel` of `serve`'s, so `serve`'s end does not reach the runs
until the supervisor cancels the root itself with `errShutdown`, and that is
the cause every run then reads. Under one mutex it holds one entry per
registered run (the run's context, from `context.WithCancelCause` over the
root with the caller's deadline on it, the instant of its first cancel, and
its phase, `working` or `finishing`), the admission counter of §6, and a
`sync.WaitGroup`. A run is registered before its first record (step 8),
which is also where the `WaitGroup` is added to, and unregistered, with the
`WaitGroup` done, only after its terminal record. So every run this process
recorded as in flight, from its first record to its last, is one
`forja.cancel` reaches, and a stop waits for every registered run, started or
not. The phase moves from `working` to `finishing`, under the mutex, when the
run's work returns and before its release stage. Each run executes the stages
it executes today, in one goroutine the supervisor started. The stages, their
records and the release are unchanged; only the context the work runs under
changes, from the request's to the run's. The contract's wall clock stays
where it is applied today, after the clone (`implementer.prepare`,
`internal/mcp/implement_agent.go`; `internal/mcp/verify_stages.go`), on the
contract the clone reads; the caller's deadline is on the run's context, so
whichever fires first is the cause the reason is read from
(`context.Cause`).

The client follows the run with `forja.status` and its `run_id`. Polling is
the protocol: Forja sends no notification and never calls the client (ADR
0051 §3). GitHub stays the rendezvous for a role that reads only the
statuses (roadmap, item 5).

**What the request's cancellation still does.** `PropagateRequestCancellation`
stays on. A request that ends during steps 1 to 6 stops the call with nothing
done but the slot given back. One that ends during step 7 is the claim's own
case: the claim settles its answer within `claim.SettleBound` and gives back a
ref it took but cannot hand over, as today (`internal/claim/mutation.go`). One
that ends after step 7 changes nothing: the run carries on. A dropped request therefore no longer
cancels a run; only `forja.cancel` (§2), the run's deadline (§4) and a stop of
`forjad` do.

**A stop of `forjad`.** The supervisor refuses new registrations once
`serve`'s context ends. It then cancels every registered run with the cause
`errShutdown`, and `serve` waits for the supervisor and for the HTTP drain
under the one drain budget it has today (195 s, ADR 0052 §7). The runs tear
down in parallel, each within the bound that already exists, so the drain is
unchanged and so is the requirement on the pod's
`terminationGracePeriodSeconds` (above 230 s, 240 s when the chart lands). A
start call refused at step 8 by the stop leaves nothing (step 8). A start call
registered before the stop has its run cancelled like any other: its
goroutine starts on an ended context, starts no stage, releases the claim and
records the run `failed`, and the call answers the `RunStarted` it recorded,
so a retry with its key after the restart replays that run. A run cancelled by
the stop is recorded `failed` with reason `interrupted`, the reason recovery
already uses for a run a dead process left (ADR 0076 §5). On stdio, `serve` does the same
when the transport closes, so a client that ends its session ends its runs,
as it does today.

**A run does not resume.** A run in flight when `forjad` stops ends `failed`
with reason `interrupted`, recorded by the stop. A run in flight when
`forjad` dies ends the same way only if its claim was journaled: the next
start's recovery closes the run of every journaled claim and nothing else
(ADR 0076 §5). Its record, and its idempotency key with it, stay readable
after the restart; a new attempt takes a new key. That is how the roadmap's
first M2 criterion ("reconnects after a `forjad` restart, and reads the same
run to its end") is met: the end it reads is `interrupted` when the restart
came first.

The gap ADR 0076 names stays open: with `FORJA_STATE_DIR` unset there is no
journal, and in the windows ADR 0076 lists (an implement run that died after
its release removed the journal entry but before its last record) a run that
died stays `running` in the store. Its claim's lease still frees the issue.
Closing those runs needs the run to record its owner and a liveness lease,
which this ADR does not add (Open question 3 makes the state directory a
requirement of HTTP, which closes the first case). A replay of such a run
answers `running` for as long as the row says so; `forja.cancel` answering
`not_held` (§2) is the signal that no live run of this process holds it, and
the client starts a new operation, with a new key, once the claim's lease has
freed the issue.

**`forja.doctor` stays synchronous.** It holds no claim, it is read-class, and
the operator calls it while setting a repository up. It counts against
admission (§6), because it launches a workspace. Its blocking is one of the
reasons HTTP keeps one protocol version (§8).

### 2. `forja.cancel`

A new write-class tool. Input: a `run_id`. It stops a run the calling client
owns.

- **Who.** Only the client that started the run (`domain.Run.Client`), read
  with `GetOwned`. Another client's run, a run the store does not know and an
  id that never existed are the same tool error, byte for byte: the error
  `forja.cancel` answers for an id that never existed, built the way
  `forja.status` builds its own (`internal/mcp/status_owned.go`), with
  `forja.cancel` as its tool prefix (ADR 0052 §4). The stdio client owns its
  own runs. There is no cross-client or operator cancel through MCP.
- **What it does to a run in flight in this process.** The supervisor records
  the instant of the first cancel and cancels the run's context with the
  cause `errCancelled`. The cancel call never writes the run's row: the run's
  goroutine is the only writer of its record (`flight`, "One goroutine owns
  it", `internal/mcp/implement_stages.go`), and the store upserts the whole
  document (`internal/runstore/postgres.go`, `Put`), so a second writer would
  lose fields to the next record. The goroutine copies `cancel_requested_at`
  from the supervisor into its own record on its next write. The answer of
  `forja.cancel` takes the instant from the supervisor, so it does not wait
  for that write. From there the run takes the path a run
  whose caller left takes today: no stage starts, the stage in flight finishes
  on its own detached budgets, the workspace is removed, the claim is
  released and the run is recorded `failed` with the new reason `cancelled`.
  That is within the existing teardown bound, 180 s from the cancel
  (`teardownBound`).
- **What it answers.** It returns as soon as the cancel is delivered, without
  waiting for the teardown. After the ownership read, it decides under the
  supervisor's mutex, and only then reads the row again:
  - registered, phase `working`, context live: it cancels, `cancelling`; a
    run a previous cancel already reached is also `cancelling`;
  - registered, phase `finishing`, or context already ended for another
    cause (the caller's deadline, the wall clock, a stop): `ending`, nothing
    changed; the run keeps the outcome its work and release give it, so a
    run whose work delivered and whose release succeeds ends `succeeded`;
  - not registered: the row, read after the miss, decides. Terminal is
    `already_terminal` (the record unchanged); `queued` or `running` is
    `not_held` (below).

  The order makes the miss sound. A run is registered before its first record
  and unregistered after its last, and a client learns a `run_id` only from
  `RunStarted` or a replay, both after registration. So a run of this process
  missing from the registry has either not been recorded in a way the client
  could know, or already has its terminal record. A cancel is idempotent: a
  second call answers the same outcome for the same run state.
- **A run this process does not hold.** With one replica (the chart runs one
  until the roadmap's open question on replicas is decided), a non-terminal
  run this process does not hold is a run a dead process left without a
  journaled claim (ADR 0076, Consequences), or one another `forjad` on the
  same database runs, such as a second stdio session. Forja cannot tell the
  two apart, so it changes nothing: `not_held`, the record unchanged. The
  claim's lease bounds what such a run can still hold.
- **What is guaranteed released, and recorded.** For a `cancelling` run:
  every container and network of the run's workspace, the claim ref
  `refs/forja/claims/<n>`, and the terminal record `failed` / `cancelled`
  with `finished_at`. The run keeps every stage it recorded, with the stage in
  flight ended `failed`. Recorded beside it: `cancel_requested_at`. What
  Forja already wrote into the repository stays: a branch it pushed, a pull
  request it opened, a comment and a label. No stage starts once the cancel
  is delivered, so a cancelled run publishes `forja/gates` or `forja/qa`
  only if its `status` stage had already started; the run is still recorded
  `failed`, `cancelled`, and its record says which stages ended. A run
  cancelled between its push and its pull
  request leaves the pushed branch behind, as ADR 0052 §7 says of a stop.
- **Precedence.** The run's context keeps the first cause that ended it. A
  run whose work has returned is no longer cancellable (`ending`, or
  `already_terminal` once recorded); one whose deadline or stop came first
  keeps that reason (`ending`); a cancel that came first wins over a deadline
  that would have ended the run later (`cancelling`, reason `cancelled`). A
  run answered `cancelling` always ends `failed` / `cancelled`. The terminal
  record reads the run context's cause: `lifecycle.run`, which today decides
  `succeeded` from the work and the release alone
  (`internal/mcp/lifecycle.go`), records `failed` / `cancelled` whenever the
  cause is `errCancelled`, even if the stage in flight completed.
- **Logs.** The operator log carries the client, the tool and the `run_id`.

### 3. `caller_ref` and `idempotency_key`

Two optional inputs of `forja.implement` and `forja.verify`, kept apart as the
roadmap (item 6) keeps them.

**`caller_ref` is correlation.** An opaque string, 1 to 256 bytes, text in the
sense of `domain.IsText` (valid UTF-8, no NUL). Forja never parses it, never
deduplicates on it and never logs it. It is recorded on the run
(`domain.Run.CallerRef`), returned by `forja.status` and `RunStarted`, and
`forja.status` lists can filter on it, always within the calling client's
runs. Many runs may carry the same `caller_ref`. It is not written into the
evidence, the pull request or the commit statuses, as the client's name is not
(ADR 0052 §5).

**`idempotency_key` is one operation.** A string of 1 to 128 bytes of
printable ASCII (`0x21` to `0x7E`), chosen by the caller for one attempt at one
tool call with one set of arguments. It is not logged.

- **Scope.** The key's identity is `(client, idempotency_key)`. Two clients'
  keys never collide and never see each other: client B using A's key starts
  its own run, and nothing in B's answer depends on A's key existing.
- **Arguments hash.** Forja stores with the key a SHA-256 over the tool's name,
  a newline, and the canonical encoding of its input without
  `idempotency_key` and `caller_ref`: the decoded Go input struct, normalised,
  re-encoded with `encoding/json`, so field order and whitespace are fixed by
  the struct and not by the client. Normalisation makes every spelling of one
  operation the same input: a field absent, `null` or at its zero value (`""`,
  `0`, `{}`) is absent, so `budget: {}` and no `budget` hash alike, and a
  `deadline` is parsed and re-encoded in UTC (RFC3339 with nanoseconds). The
  hashed struct is its own type, separate from the wire input, and every
  optional field of it is tagged `omitzero` (Go 1.24, `go.mod` is on 1.25),
  which omits a zero `time.Time` and an empty struct where `omitempty` would
  not. So an optional field added in a later release is absent from the hash
  of every call that does not set it, and keys stored before the upgrade still
  replay. The budget and the deadline are part of the operation, so they are
  in the hash; `caller_ref` is not (below).
- **Same key, same arguments.** The same `run_id`, in whatever state the run
  is, as a `RunStarted` with `replayed: true`. No second run, no second claim,
  no second claim commit. This holds after a `forjad` restart, since the key
  is in the durable store. A replay is answered at step 2 of §1, so it needs
  no capacity, reads no contract and touches no claim. A replay keeps the
  first call's `caller_ref`: `caller_ref` is correlation, not part of the
  operation (roadmap, item 6), so a different one does not conflict, and
  `RunStarted.caller_ref` returns the recorded one so the caller sees which
  correlation the run carries.
- **Same key, different arguments.** A tool error with the code
  `idempotency_key_conflict` naming the existing `run_id` (the caller's own
  run, so naming it discloses nothing, ADR 0052 §4). No run, no claim. Using
  `K1` of a first run for its correction round is a conflict, because the
  `branch` and `finding` differ.
- **A refused call stores nothing.** A call refused before its run was
  recorded (a held claim, a red admission, a contract that does not parse)
  leaves no key behind. The same key then starts a run on a later call. A
  call whose run was recorded keeps its key even if the run then failed: a
  new attempt takes a new key.
- **Two calls with the same key at once.** The key is written in the same
  transaction as the run's first record, under the primary key
  `(client, idempotency_key)`. With the same arguments, both calls name the
  same issue and the claim serialises them: the loser is refused as held by a
  run of its own client, reads the key again and, when the winner's key is
  there, answers the replay; when the winner has not recorded it yet, the loser
  answers the held claim, which names its own client's `run_id` (ADR 0052 §4,
  P4). With different arguments on the same issue (a first run and its
  correction round under one key), the loser is refused by the held claim the
  same way, and the stored key, once there, decides: its hash differs, so the
  answer is `idempotency_key_conflict`, which takes precedence over the held
  claim. In every other order the loser holds a claim of its own when its step 9
  transaction hits the primary key: with different arguments it held another
  issue; with the same arguments the winner's run ended and released the
  issue before the loser claimed it. Either way the loser unregisters, releases
  its claim, records no run and answers from the stored key: the replay when
  the hashes match, `idempotency_key_conflict` when they do not. In those races
  only, a claim commit of the loser appears and is deleted within the release
  bound; there is never a second run.
- **Retention.** The key lives as long as the run's record: the key row
  references the run row with `ON DELETE CASCADE`. Forja deletes no run today,
  so no key expires.

**Migration `0003_caller_ref_and_idempotency.sql`:**

```sql
-- The caller's correlation reference, opaque to Forja; '' when none was given.
ALTER TABLE forja_runs ADD COLUMN caller_ref text NOT NULL DEFAULT '';
CREATE INDEX forja_runs_client_caller_ref
    ON forja_runs (client, caller_ref, started_at DESC);

-- One operation of one client: the run it started and the hash of the
-- arguments it started it with.
CREATE TABLE forja_idempotency_keys (
    client           text        NOT NULL,
    key              text        NOT NULL,
    tool             text        NOT NULL,
    arguments_sha256 bytea       NOT NULL CHECK (octet_length(arguments_sha256) = 32),
    run_id           text        NOT NULL REFERENCES forja_runs (id) ON DELETE CASCADE,
    created_at       timestamptz NOT NULL,
    PRIMARY KEY (client, key)
);
```

`created_at` comes from the injected clock, not from the database's. The run
store gains one write that inserts or updates the run's row and inserts the
key in one transaction, and returns a typed error naming the existing run
when the key is taken; and one read of a key by `(client, key)`. Both stores
implement them, and the behaviour suite of ADR 0076 §6 runs against both. The
in-memory store keeps keys for the life of the process only: idempotency
across a restart needs `FORJA_DATABASE_URL` (Open question 3).

### 4. The caller's deadline and budget

Two optional inputs of `forja.implement` and `forja.verify`. They can only
lower a ceiling, never raise one.

- **`deadline`**, an RFC3339 instant. The work's deadline becomes the earlier
  of the claim's time plus `limits.wall_clock` (as `lifecycle.timeLeft`
  computes it today) and `deadline`. The lease is still sized from the
  contract alone (`domain.LeaseFor`), so a caller cannot make a run outlive
  its lease. A `deadline` at or before the moment of the call is refused
  before the claim with the code `deadline_passed`. A deadline that ends the
  run is the new reason `deadline`; one equal to the contract's wall clock is
  the contract's, `wall_clock`. A deadline is absolute rather than a duration
  so that a retry with the same arguments hashes the same (§3) and does not
  extend the run.
- **`budget.max_tokens`**, a positive integer: the total tokens, input and
  output, the run may use, counted as `FORJA_MODEL_TOKEN_CAP` counts them
  today. The run's cap becomes the lower of the two. A run stopped by the
  caller's budget is the new reason `token_budget`; one stopped by the
  operator's cap, or by a cap that ties, stays `token_cap`.
- **No money budget.** Forja's cost is an estimate from the operator's price
  table, computed when a run is answered and absent without one (`internal/
  mcp/cost.go`), so it cannot be enforced while the run works. The caller
  converts its money budget into tokens and a deadline (Open question 4).

The effective ceilings are on the run (`limits` in `forja.status`, and in
`RunStarted` as far as they are known when the call answers), with the
caller's own values beside them, so the caller can
see which ceiling applied.

### 5. `forja.status` separates structured fields from untrusted ones

The record `forja.status` answers is split in two:

- **Structured** (the top level): everything the control plane produced and
  can type. States, reasons, verdicts, stage names and outcomes are enums;
  SHAs match `^[0-9a-f]{40}$`; counts, exit codes, durations, tokens, compute
  and cost are numbers; instants are RFC3339. The branch is `forja/<issue>`
  and the pull request's URL is GitHub's. Gate and setup step names and
  commands come from the base commit's contract (CLAUDE.md rule 2), and a
  step's `reason` is the control plane's own one-line account
  (`domain.GateResult.Reason`); both stay structured.
- **`untrusted`**: everything the agent or the agent's code could have
  shaped. Each step's `stdout`, `stderr` and `truncated`; every path the
  agent's head changed (`proof_apparatus`, `human_merge_paths`, `excluded`,
  each inversion's `paths`); each refused egress destination; each registry
  path; and the rendered evidence, which is the pull request's body. The
  structured side keeps a count for each list, so a caller knows what it did
  not read.

`untrusted` is absent unless the call sets `include_untrusted: true`. A role
that must stay untainted calls `forja.status` without it and never receives a
byte the agent influenced. The same split applies to a list:
`list.repositories[].egress_denied`, which names destinations, moves to
`untrusted.repositories`.

`forja.implement` and `forja.verify` no longer return the outcome, so their
outputs become `RunStarted`, which is structured only. The evidence, the
gates and the inversions are read from `forja.status`.

### 6. Admission

`FORJA_MAX_RUNS`, a positive integer, is how many runs this process may have
holding a workspace slot at once. A slot is taken at step 4 of §1. It is given
back by a call refused after step 4, by a step 9 that fails, and otherwise
when the run's release stage has ended, so a run that is tearing down still
holds one. `forja.doctor` takes its slot before it clones and gives it back
when its workspace is gone, on every outcome. `forja.implement`, `forja.verify` and `forja.doctor` take a
slot; `forja.plan`, `forja.status` and `forja.cancel` do not. A call when
every slot is taken is **refused**, not queued: a tool error with the code
`at_capacity`, before the contract is read or the claim attempted; no run,
no claim, no key. The text names the limit and no other client's runs.

Unset, there is no limit, as today; the chart sets it. A refusal is
back-pressure, not scheduling (roadmap, M2): FuseOne schedules its cards and
retries with the same key, which does not conflict because nothing was stored
(§3). The count is per process and is exported as two gauges of its own,
`forja_run_slots` (the limit, absent when unset) and `forja_run_slots_in_use`.
The existing `forja_runs{state="in_flight"}` is not that count: it is read from
the run store per repository (`internal/mcp/status_metrics.go`,
`internal/domain/metrics.go`), so it includes rows a dead process left and
runs of another process on the same database.
Queuing is rejected under Alternatives. `queued` stays a state no run takes.

### 7. Named tool errors

The new refusals carry a stable code, so a client branches on the code and
never on prose. A tool error's text is `<tool>: <code>: <detail>`; the code set
is part of the public contract:

| Code | Tools | When | Names |
|---|---|---|---|
| `idempotency_key_conflict` | implement, verify | §3 | the existing `run_id` |
| `at_capacity` | implement, verify, doctor | §6 | the limit |
| `deadline_passed` | implement, verify | §4 | the deadline |

Errors that exist today keep their text. That includes the unknown-run error
of `forja.status` (`internal/mcp/status_owned.go`), which `forja.cancel`
answers with its own tool prefix: giving it a code is a change of text, and of
the byte-for-byte rule of ADR 0052 §4, left to a later ADR. The held claim's
rendering is #60's (ADR 0052 §4).

### 8. HTTP keeps protocol 2026-07-28 only

Asynchronous runs remove the reason ADR 0052 §7 gave for `implement` and
`verify`: a dropped request no longer needs to stop a run. HTTP still accepts
2026-07-28 only, because:

- `forja.doctor` still blocks for up to the contract's wall clock and runs a
  workspace (§1); on 2025-11-25 a dropped caller would not stop it (ADR 0052
  §7, `notDone`).
- No client needs another version: FuseOne's entry is `auto` on 2026-07-28
  (ADR 0052, maintainer decision 1).
- Widening later is additive and fits a minor release; narrowing after
  v1.0.0 would be a major one. The safe order is narrow now.

The middleware that answers `400` without the header, and
`PropagateRequestCancellation`, stay. stdio keeps the SDK's full list.

### Tool schemas

Each tool's schema below is one document, with its `$defs` inside it; the
golden files the SDK generates (`internal/mcp/testdata/tools/`,
`internal/mcp/schema_golden_test.go`) inline them. `RunStarted` is written
once and is the `outputSchema` of both `forja.implement` and `forja.verify`,
copied into each. The lengths, character sets and minimums the prose states
are in the schemas, so a client that validates against them is refused
nothing the schema allowed; the server enforces byte lengths, which a JSON
Schema `maxLength` counts in characters. `forja.doctor` and `forja.plan` are
unchanged.

#### `forja.implement`

Annotations unchanged: `readOnlyHint: false`, `destructiveHint: true`,
`idempotentHint: false` (a call is idempotent only with a key).

```json
{
  "name": "forja.implement",
  "description": "Start implementing an issue: claims it, starts the run and returns its run_id; forja.status follows it to the branch, the gates and the PR with forja/gates.",
  "inputSchema": {
    "type": "object",
    "additionalProperties": false,
    "required": ["repository", "issue", "base_branch"],
    "properties": {
      "repository": { "type": "string", "description": "repository in owner/name form" },
      "issue": { "type": "integer", "description": "issue number to implement" },
      "base_branch": { "type": "string", "description": "branch the PR targets" },
      "branch": { "type": "string", "description": "for a correction round: forja/<issue>, the branch of the pull request Forja opened; set together with finding" },
      "finding": { "type": "string", "description": "for a correction round: the review finding to address, at most 64 KiB; set together with branch" },
      "caller_ref": { "type": "string", "minLength": 1, "maxLength": 256, "description": "the caller's correlation reference, 1 to 256 bytes, recorded on the run and never deduplicated" },
      "idempotency_key": { "type": "string", "pattern": "^[!-~]{1,128}$", "description": "one operation of this client, 1 to 128 printable ASCII bytes; the same key with the same arguments returns the same run" },
      "deadline": { "type": "string", "format": "date-time", "description": "RFC3339 instant the run's work must end by; the earlier of it and the contract's wall clock applies" },
      "budget": { "$ref": "#/$defs/Budget" }
    },
    "$defs": {
      "Budget": {
        "type": "object",
        "additionalProperties": false,
        "properties": {
          "max_tokens": { "type": "integer", "minimum": 1, "description": "total model tokens the run may use; the lower of it and the operator's cap applies" }
        }
      }
    }
  },
  "outputSchema": "<the RunStarted schema below, verbatim>"
}
```

#### `forja.verify`

Annotations unchanged.

```json
{
  "name": "forja.verify",
  "description": "Start verifying a pull request a run opened against the base commit's contract and return its run_id; forja.status follows it to the verdict and forja/qa on its head.",
  "inputSchema": {
    "type": "object",
    "additionalProperties": false,
    "required": ["repository", "pull_request"],
    "properties": {
      "repository": { "type": "string", "description": "repository in owner/name form" },
      "pull_request": { "type": "integer", "description": "pull request number to verify" },
      "caller_ref": { "type": "string", "minLength": 1, "maxLength": 256, "description": "the caller's correlation reference, 1 to 256 bytes, recorded on the run and never deduplicated" },
      "idempotency_key": { "type": "string", "pattern": "^[!-~]{1,128}$", "description": "one operation of this client, 1 to 128 printable ASCII bytes; the same key with the same arguments returns the same run" },
      "deadline": { "type": "string", "format": "date-time", "description": "RFC3339 instant the run's work must end by; the earlier of it and the contract's wall clock applies" },
      "budget": { "$ref": "#/$defs/Budget" }
    },
    "$defs": {
      "Budget": {
        "type": "object",
        "additionalProperties": false,
        "properties": {
          "max_tokens": { "type": "integer", "minimum": 1, "description": "total model tokens the run may use; the lower of it and the operator's cap applies" }
        }
      }
    }
  },
  "outputSchema": "<the RunStarted schema below, verbatim>"
}
```

#### `RunStarted`, the output of `implement` and `verify`

```json
{
  "type": "object",
  "additionalProperties": false,
  "required": ["run_id", "tool", "repository", "issue", "state", "started_at", "replayed", "limits"],
  "properties": {
    "run_id": { "type": "string" },
    "tool": { "enum": ["forja.implement", "forja.verify"] },
    "repository": { "type": "string" },
    "issue": { "type": "integer" },
    "state": { "$ref": "#/$defs/State" },
    "started_at": { "type": "string", "format": "date-time" },
    "replayed": { "type": "boolean", "description": "true when the idempotency key named a run already started" },
    "caller_ref": { "type": "string" },
    "supersedes": { "type": "string", "description": "the caller's own run whose expired claim this run took over; absent otherwise (ADR 0052 §4)" },
    "pull_request": { "$ref": "#/$defs/PullRequest", "description": "verify: the pull request verified" },
    "head_sha": { "$ref": "#/$defs/SHA", "description": "verify: the pull request's head" },
    "limits": { "$ref": "#/$defs/Limits" }
  },
  "$defs": {
    "State": { "enum": ["queued", "running", "succeeded", "failed"] },
    "SHA": { "type": "string", "pattern": "^[0-9a-f]{40}$" },
    "PullRequest": {
      "type": "object",
      "additionalProperties": false,
      "required": ["number", "url"],
      "properties": { "number": { "type": "integer" }, "url": { "type": "string" } }
    },
    "Limits": {
      "type": "object",
      "additionalProperties": false,
      "description": "as known when the call answers: the contract read before the claim (step 5) and the caller's values. The run works under the contract its clone reads at the base commit, which may differ; forja.status reports the limits the run works under",
      "properties": {
        "wall_clock_seconds": { "type": "number", "description": "the base contract's wall clock" },
        "max_diff_lines": { "type": "integer", "description": "implement: the base contract's limit" },
        "max_attempts": { "type": "integer", "description": "implement: the base contract's limit" },
        "deadline": { "type": "string", "format": "date-time", "description": "the effective end of the work: the earlier of the claim plus the wall clock and the caller's deadline" },
        "max_tokens": { "type": "integer", "description": "the effective token cap; absent when the run has no agent" },
        "caller_deadline": { "type": "string", "format": "date-time" },
        "caller_max_tokens": { "type": "integer" }
      }
    }
  }
}
```

Example, a first call and its replay:

```json
{"run_id":"r-01JC3S8M","tool":"forja.implement","repository":"acme/payments","issue":7,
 "state":"running","started_at":"2026-11-06T14:02:11.204Z","replayed":false,
 "caller_ref":"card-412/run-9f2","limits":{"wall_clock_seconds":1800,"max_diff_lines":400,
 "max_attempts":3,"deadline":"2026-11-06T14:22:11Z","max_tokens":200000,
 "caller_deadline":"2026-11-06T14:22:11Z","caller_max_tokens":200000}}
```

```json
{"run_id":"r-01JC3S8M","tool":"forja.implement","repository":"acme/payments","issue":7,
 "state":"succeeded","started_at":"2026-11-06T14:02:11.204Z","replayed":true,
 "caller_ref":"card-412/run-9f2","limits":{"wall_clock_seconds":1800,"max_diff_lines":400,
 "max_attempts":3,"deadline":"2026-11-06T14:22:11Z","max_tokens":200000,
 "caller_deadline":"2026-11-06T14:22:11Z","caller_max_tokens":200000}}
```

The conflict, as tool error text:

```
forja.implement: idempotency_key_conflict: key already started run r-01JC3S8M with other arguments
```

#### `forja.cancel` (new)

Annotations: `readOnlyHint: false`, `destructiveHint: true`,
`idempotentHint: true`. Class `write` in FuseOne's Gate (roadmap, M3).

```json
{
  "name": "forja.cancel",
  "description": "Cancel a run this client started: no stage starts, the workspace is removed, the claim released and the run recorded failed with reason cancelled.",
  "annotations": { "readOnlyHint": false, "destructiveHint": true, "idempotentHint": true },
  "inputSchema": {
    "type": "object",
    "additionalProperties": false,
    "required": ["run_id"],
    "properties": {
      "run_id": { "type": "string", "description": "identifier of the run to cancel" }
    }
  },
  "outputSchema": {
    "type": "object",
    "additionalProperties": false,
    "required": ["run_id", "outcome", "state"],
    "properties": {
      "run_id": { "type": "string" },
      "outcome": {
        "enum": ["cancelling", "ending", "already_terminal", "not_held"],
        "description": "cancelling: the cancel reached the run, which tears down and ends failed, cancelled; ending: the run was already tearing down for another cause, whose reason it keeps; already_terminal: the run had ended and is unchanged; not_held: the run is recorded in flight but no run registered in this process is it, and it is unchanged"
      },
      "state": { "enum": ["queued", "running", "succeeded", "failed"], "description": "the state recorded when the cancel answered" },
      "cancel_requested_at": { "type": "string", "format": "date-time", "description": "when the first cancel reached the run; present for cancelling, and for already_terminal when a cancel ended the run" }
    }
  }
}
```

```json
{"run_id":"r-01JC3S8M","outcome":"cancelling","state":"running","cancel_requested_at":"2026-11-06T14:09:40Z"}
```

#### `forja.status`

Annotations unchanged (`readOnlyHint: true`).

```json
{
  "name": "forja.status",
  "description": "With run_id: the run's structured record (state, reasons, stages, exit codes, SHAs, limits, cost), and with include_untrusted its agent-influenced fields under untrusted. Without run_id: the caller's runs that match the filters, newest first, one page at a time, under list.",
  "inputSchema": {
    "type": "object",
    "additionalProperties": false,
    "properties": {
      "run_id": { "type": "string", "description": "identifier of one run; omit it to list runs" },
      "include_untrusted": { "type": "boolean", "description": "also return, under untrusted, the fields the agent or its code could have shaped: logs, paths, egress destinations, registry paths, evidence" },
      "repository": { "type": "string", "description": "list only the runs of this repository, in owner/name form" },
      "tool": { "type": "string", "description": "list only the runs of this tool, such as forja.implement" },
      "state": { "type": "string", "description": "list only the runs in this state: queued, running, succeeded or failed" },
      "caller_ref": { "type": "string", "description": "list only the runs recorded with this caller_ref" },
      "since": { "type": "string", "description": "RFC3339 instant; list only the runs started at or after it" },
      "until": { "type": "string", "description": "RFC3339 instant; list only the runs started before it" },
      "limit": { "type": "integer", "description": "runs per page, 1 to 200; 50 when omitted" },
      "cursor": { "type": "string", "description": "next_cursor of the previous page" }
    }
  },
  "outputSchema": {
    "type": "object",
    "additionalProperties": false,
    "description": "Exactly one of a run or a list. A call with run_id carries the run's fields and no list; a call without carries only list (and untrusted with include_untrusted). The run fields are absent, not zero, in a list answer, so their enums never meet an empty string.",
    "oneOf": [
      { "required": ["id", "tool", "repository", "issue", "client", "state", "started_at",
          "tokens_in", "tokens_out", "compute_minutes", "stages", "setup", "gates",
          "attempt", "max_attempts", "attempts", "human_merge",
          "egress_denied_count", "registry_access_count", "proof_apparatus_count",
          "human_merge_paths_count", "excluded_count", "inversions"],
        "not": { "required": ["list"] } },
      { "required": ["list"], "not": { "required": ["id"] } }
    ],
    "properties": {
      "id": { "type": "string" },
      "tool": { "enum": ["forja.doctor", "forja.plan", "forja.implement", "forja.verify", "forja.status"] },
      "repository": { "type": "string" },
      "issue": { "type": "integer" },
      "client": { "type": "string" },
      "caller_ref": { "type": "string" },
      "state": { "enum": ["queued", "running", "succeeded", "failed"] },
      "failure_reason": { "enum": ["gate_failed", "no_commits", "diff_too_large", "token_cap", "token_budget", "wall_clock", "deadline", "cancelled", "interrupted", "inversion_unavailable", "inversion_inconclusive"] },
      "started_at": { "type": "string", "format": "date-time" },
      "finished_at": { "type": "string", "format": "date-time" },
      "cancel_requested_at": { "type": "string", "format": "date-time" },
      "supersedes": { "type": "string" },
      "limits": { "$ref": "#/$defs/Limits" },
      "base_sha": { "$ref": "#/$defs/SHA" },
      "contract_sha": { "$ref": "#/$defs/SHA" },
      "start_sha": { "$ref": "#/$defs/SHA" },
      "head_sha": { "$ref": "#/$defs/SHA" },
      "branch": { "type": "string", "pattern": "^forja/[1-9][0-9]*$" },
      "pull_request": { "$ref": "#/$defs/PullRequest" },
      "diff_lines": { "type": "integer" },
      "tokens_in": { "type": "integer" },
      "tokens_out": { "type": "integer" },
      "compute_minutes": { "type": "number" },
      "estimated_cost_usd_micros": { "type": "integer" },
      "stages": { "type": "array", "items": { "$ref": "#/$defs/Stage" } },
      "setup": { "type": "array", "items": { "$ref": "#/$defs/Step" } },
      "gates": { "type": "array", "items": { "$ref": "#/$defs/Step" } },
      "attempt": { "type": "integer" },
      "max_attempts": { "type": "integer" },
      "attempts": { "type": "array", "items": { "$ref": "#/$defs/Attempt" } },
      "human_merge": { "type": "boolean" },
      "proof_apparatus_count": { "type": "integer" },
      "human_merge_paths_count": { "type": "integer" },
      "egress_denied_count": { "type": "integer" },
      "registry_access_count": { "type": "integer" },
      "verdict": { "enum": ["bites", "does_not_bite", "not_verifiable"] },
      "verdict_reason": { "enum": ["only_excluded_paths", "no_production_code"] },
      "excluded_count": { "type": "integer" },
      "inversions": { "type": "array", "items": { "$ref": "#/$defs/Inversion" } },
      "list": { "$ref": "#/$defs/RunList" },
      "untrusted": { "$ref": "#/$defs/Untrusted" }
    },
    "$defs": {
      "SHA": { "type": "string", "pattern": "^[0-9a-f]{40}$" },
      "PullRequest": {
        "type": "object",
        "additionalProperties": false,
        "required": ["number", "url"],
        "properties": { "number": { "type": "integer" }, "url": { "type": "string" } }
      },
      "Limits": {
        "type": "object",
        "additionalProperties": false,
        "description": "absent until the run read its contract, and for forja.doctor and forja.plan runs; deadline and the caller's fields are absent on rows recorded before migration 0003",
        "properties": {
          "wall_clock_seconds": { "type": "number" },
          "max_diff_lines": { "type": "integer" },
          "max_attempts": { "type": "integer" },
          "deadline": { "type": "string", "format": "date-time" },
          "max_tokens": { "type": "integer" },
          "caller_deadline": { "type": "string", "format": "date-time" },
          "caller_max_tokens": { "type": "integer" }
        }
      },
      "Stage": {
        "type": "object",
        "additionalProperties": false,
        "required": ["name", "started_at", "outcome", "compute_minutes", "tokens_in", "tokens_out"],
        "properties": {
          "name": { "enum": ["claim", "clone", "agent", "gates", "diff", "inversion", "push", "pull_request", "comment", "status", "release"] },
          "started_at": { "type": "string", "format": "date-time" },
          "finished_at": { "type": "string", "format": "date-time" },
          "outcome": { "enum": ["running", "succeeded", "failed"] },
          "attempt": { "type": "integer" },
          "compute_minutes": { "type": "number" },
          "tokens_in": { "type": "integer" },
          "tokens_out": { "type": "integer" }
        }
      },
      "Step": {
        "type": "object",
        "additionalProperties": false,
        "required": ["name", "command", "status", "exit_code", "duration"],
        "properties": {
          "name": { "type": "string" },
          "command": { "type": "string" },
          "status": { "enum": ["passed", "failed", "skipped", "timed_out"] },
          "exit_code": { "type": "integer" },
          "duration": { "type": "integer" },
          "reason": { "type": "string" }
        }
      },
      "Attempt": {
        "type": "object",
        "additionalProperties": false,
        "required": ["number", "tokens_in", "tokens_out", "setup", "gates"],
        "properties": {
          "number": { "type": "integer" },
          "head_sha": { "$ref": "#/$defs/SHA" },
          "failed_step": { "type": "string" },
          "tokens_in": { "type": "integer" },
          "tokens_out": { "type": "integer" },
          "setup": { "type": "array", "items": { "$ref": "#/$defs/Step" } },
          "gates": { "type": "array", "items": { "$ref": "#/$defs/Step" } }
        }
      },
      "Inversion": {
        "type": "object",
        "additionalProperties": false,
        "required": ["branch", "admitted", "path_count"],
        "properties": {
          "branch": { "type": "string" },
          "commit": { "$ref": "#/$defs/SHA" },
          "admitted": { "type": "boolean" },
          "rejected": { "enum": ["empty", "touches_non_production", "outside_the_change", "reverts_whole_diff"] },
          "outcome": { "enum": ["bit", "escaped"] },
          "failed_step": { "type": "string" },
          "path_count": { "type": "integer" }
        }
      },
      "RunList": {
        "type": "object",
        "additionalProperties": false,
        "required": ["runs", "repositories"],
        "properties": {
          "next_cursor": { "type": "string" },
          "runs": {
            "type": "array",
            "items": {
              "type": "object",
              "additionalProperties": false,
              "required": ["id", "tool", "repository", "issue", "state", "started_at", "tokens_in", "tokens_out", "compute_minutes", "egress_denied_count"],
              "properties": {
                "id": { "type": "string" },
                "tool": { "type": "string" },
                "repository": { "type": "string" },
                "issue": { "type": "integer" },
                "caller_ref": { "type": "string" },
                "state": { "enum": ["queued", "running", "succeeded", "failed"] },
                "failure_reason": { "enum": ["gate_failed", "no_commits", "diff_too_large", "token_cap", "token_budget", "wall_clock", "deadline", "cancelled", "interrupted", "inversion_unavailable", "inversion_inconclusive"] },
                "started_at": { "type": "string", "format": "date-time" },
                "finished_at": { "type": "string", "format": "date-time" },
                "tokens_in": { "type": "integer" },
                "tokens_out": { "type": "integer" },
                "compute_minutes": { "type": "number" },
                "estimated_cost_usd_micros": { "type": "integer" },
                "egress_denied_count": { "type": "integer" }
              }
            }
          },
          "repositories": {
            "type": "array",
            "items": {
              "type": "object",
              "additionalProperties": false,
              "required": ["repository", "runs", "succeeded", "failed", "in_flight", "compute_minutes", "tokens_in", "tokens_out", "egress_denied_count", "failures"],
              "properties": {
                "repository": { "type": "string" },
                "runs": { "type": "integer" },
                "succeeded": { "type": "integer" },
                "failed": { "type": "integer" },
                "in_flight": { "type": "integer" },
                "average_duration_seconds": { "type": "number" },
                "compute_minutes": { "type": "number" },
                "tokens_in": { "type": "integer" },
                "tokens_out": { "type": "integer" },
                "estimated_cost_usd_micros": { "type": "integer" },
                "egress_denied_count": { "type": "integer" },
                "failures": {
                  "type": "array",
                  "items": {
                    "type": "object",
                    "additionalProperties": false,
                    "required": ["reason", "count", "rate"],
                    "properties": { "reason": { "type": "string" }, "count": { "type": "integer" }, "rate": { "type": "number" } }
                  }
                }
              }
            }
          }
        }
      },
      "Untrusted": {
        "type": "object",
        "additionalProperties": false,
        "description": "Fields the agent or the agent's code could have shaped. Absent unless include_untrusted is true.",
        "properties": {
          "evidence": { "type": "string" },
          "setup": { "type": "array", "items": { "$ref": "#/$defs/StepOutput" } },
          "gates": { "type": "array", "items": { "$ref": "#/$defs/StepOutput" } },
          "attempts": {
            "type": "array",
            "items": {
              "type": "object",
              "additionalProperties": false,
              "required": ["number", "setup", "gates"],
              "properties": {
                "number": { "type": "integer" },
                "setup": { "type": "array", "items": { "$ref": "#/$defs/StepOutput" } },
                "gates": { "type": "array", "items": { "$ref": "#/$defs/StepOutput" } }
              }
            }
          },
          "proof_apparatus": { "type": "array", "items": { "$ref": "#/$defs/ChangedPath" } },
          "human_merge_paths": { "type": "array", "items": { "$ref": "#/$defs/ChangedPath" } },
          "excluded": { "type": "array", "items": { "type": "string" } },
          "inversions": {
            "type": "array",
            "items": {
              "type": "object",
              "additionalProperties": false,
              "required": ["branch", "paths"],
              "properties": { "branch": { "type": "string" }, "paths": { "type": "array", "items": { "type": "string" } } }
            }
          },
          "egress_denied": {
            "type": "array",
            "items": {
              "type": "object",
              "additionalProperties": false,
              "required": ["at", "kind", "destination"],
              "properties": { "at": { "type": "string", "format": "date-time" }, "kind": { "type": "string" }, "destination": { "type": "string" } }
            }
          },
          "registry_access": {
            "type": "array",
            "items": {
              "type": "object",
              "additionalProperties": false,
              "required": ["at", "registry", "path", "outcome", "status"],
              "properties": {
                "at": { "type": "string", "format": "date-time" },
                "registry": { "type": "string" },
                "path": { "type": "string" },
                "outcome": { "enum": ["allowed", "denied"] },
                "status": { "type": "integer" }
              }
            }
          },
          "repositories": {
            "type": "array",
            "description": "list only: the refused egress of the listed repositories",
            "items": {
              "type": "object",
              "additionalProperties": false,
              "required": ["repository", "egress_denied"],
              "properties": {
                "repository": { "type": "string" },
                "egress_denied": {
                  "type": "array",
                  "items": {
                    "type": "object",
                    "additionalProperties": false,
                    "required": ["run_id", "at", "kind", "destination"],
                    "properties": {
                      "run_id": { "type": "string" },
                      "at": { "type": "string", "format": "date-time" },
                      "kind": { "type": "string" },
                      "destination": { "type": "string" }
                    }
                  }
                }
              }
            }
          }
        }
      },
      "StepOutput": {
        "type": "object",
        "additionalProperties": false,
        "required": ["name", "stdout", "stderr", "truncated"],
        "properties": {
          "name": { "type": "string" },
          "stdout": { "type": "string" },
          "stderr": { "type": "string" },
          "truncated": { "type": "boolean" }
        }
      },
      "ChangedPath": {
        "type": "object",
        "additionalProperties": false,
        "required": ["path", "change", "added", "deleted", "binary"],
        "properties": {
          "path": { "type": "string" },
          "change": { "type": "string" },
          "added": { "type": "integer" },
          "deleted": { "type": "integer" },
          "binary": { "type": "boolean" }
        }
      }
    }
  }
}
```

The SDK infers these from Go structs, so the arrays that are always on the
wire today stay `[]` rather than `null` for none, and the golden files keep
the SDK's `["null", "array"]` typing. The enums and the SHA pattern are added
on the Go side with `jsonschema` tags or a schema override; how is an
implementation detail, the golden file is the contract.

A structured read of a running run:

```json
{"id":"r-01JC3S8M","tool":"forja.implement","repository":"acme/payments","issue":7,
 "client":"fuseone-pilot","caller_ref":"card-412/run-9f2","state":"running",
 "started_at":"2026-11-06T14:02:11.204Z",
 "limits":{"wall_clock_seconds":1800,"max_diff_lines":400,"max_attempts":3,
   "deadline":"2026-11-06T14:22:11Z","max_tokens":200000,
   "caller_deadline":"2026-11-06T14:22:11Z","caller_max_tokens":200000},
 "base_sha":"9c1e0b3f4a7d2e5c8b6a1f0e3d4c7b2a9e8f1d0c","contract_sha":"4b2d9e8f1a0c3b6d5e7f2a1c0b9d8e7f6a5b4c3d",
 "branch":"forja/7","tokens_in":48211,"tokens_out":5120,"compute_minutes":3.412,
 "stages":[{"name":"claim","started_at":"2026-11-06T14:02:10.981Z","finished_at":"2026-11-06T14:02:11.204Z",
   "outcome":"succeeded","compute_minutes":0,"tokens_in":0,"tokens_out":0},
  {"name":"agent","started_at":"2026-11-06T14:02:19.020Z","outcome":"running","attempt":1,
   "compute_minutes":0,"tokens_in":0,"tokens_out":0}],
 "setup":[],"gates":[],"attempt":1,"max_attempts":3,"attempts":[],"human_merge":false,
 "proof_apparatus_count":0,"human_merge_paths_count":0,"egress_denied_count":0,
 "registry_access_count":2,"excluded_count":0,"inversions":[]}
```

### Domain and store changes

- `domain.Run` gains `CallerRef string`, `CancelRequestedAt *time.Time` and
  the caller's ceilings (`CallerDeadline *time.Time`, `CallerMaxTokens
  int64`); `domain.Limits` gains the effective `Deadline` and `MaxTokens`.
  Old rows decode them empty (ADR 0076, Consequences).
- `domain.FailureReason` gains `cancelled`, `deadline` and `token_budget`.
  The metrics' reason list (`domain.FailureReasons`,
  `internal/domain/metrics.go`) gains them and the two it lacks today,
  `interrupted` and `inversion_inconclusive`, so none lands in `unspecified`:
  `interrupted` becomes the reason of every run a stop ends. `forja.cancel` records no run of its own,
  so `domain.Tool`, which names the tools a run is recorded under, does not
  gain it; the tool's name is a constant of `internal/mcp`.
- `domain.RunFilter` gains `CallerRef`; the Postgres store narrows on the
  `caller_ref` column under the client, as it does on `client` (ADR 0076 §3).

## Alternatives considered

| Alternative | Why not |
|---|---|
| Keep the blocking call; let clients set long timeouts | A 30-minute open call through an ingress and a worker that can restart is the failure ADR 0052 §6 already works around. Every retry after a timeout starts a second run, and the caller cannot stop what it no longer waits on except by dropping the connection. |
| Return the `run_id` before the claim (as `queued`), and claim inside the run | A held claim, a contract that does not parse or a branch Forja did not open would become a failed run the caller has to poll for, instead of the tool error it is today. The claim takes seconds; keeping it in the call keeps every refusal synchronous and leaves no run for an operation that never started. |
| MCP's long-running tasks instead of `run_id` plus `forja.status` | Not stable in the SDK Forja uses, and not supported by FuseOne's worker (roadmap, open questions). The polling shape works with every client today, and tasks can be added later beside it without breaking it. |
| Queue at capacity, and start the run when a slot frees | A queued run either holds its claim while it waits, blocking the issue for every client, or claims later, when the contract or the base may have moved and the claim may be refused with nobody waiting for the answer. FuseOne already schedules; the roadmap asks for back-pressure, not scheduling. |
| One key for correlation and deduplication (the PRD's "caller key", work item plus caller run id) | A correction round issued from the same FuseOne run would return the first run and never run (roadmap, item 6). |
| Idempotency key as a field of the run document | A lookup by `(client, key)` would scan the `json` column, and the uniqueness two concurrent calls need could not be enforced by the database. |
| Reserve the key before the claim, with a pending row | Closes the same-key race fully, but a crash between the pending row and the run's first record leaves a key that no run owns and blocks every retry with it; cleaning it needs a liveness lease Forja does not have (ADR 0076, Consequences). |
| `cancelled` as a fifth state | Every client and the metrics already branch on four states, and "failed for a reason the control plane names" is how `wall_clock`, `token_cap` and `interrupted` are already expressed. |
| `forja.cancel` waits for the teardown | The teardown can take 180 s (`teardownBound`), longer than a cancel should hold a call open; `forja.status` reports its end. |
| Cancel a run this process does not hold by marking it failed | Another live `forjad` on the same database may be running it (ADR 0076 §5), and marking it failed would leave its workspace and claim in place while the record says they are gone. |
| A money budget enforced by Forja | Forja's cost is an estimate from an operator's price table computed on read, absent without one; enforcing it would turn the estimate into a bill. |
| Untrusted fields behind a flag on each field instead of one `untrusted` object | A caller, or FuseOne's Gate, would have to know the list of fields; one key is mechanically droppable and reviewable in the schema. |
| Widen HTTP to 2025-11-25 now that runs are asynchronous | `forja.doctor` still blocks, no client needs it, and widening later is additive (§8). |

## Consequences

- **Breaking schema changes, once, before v1.0.0.** `forja.implement` and
  `forja.verify` return `RunStarted`; `forja.status` moves agent-influenced
  fields under `untrusted`, replaces lists by counts on the structured side,
  and moves `wall_clock_seconds` and `max_diff_lines` from its top level into
  `limits`; `forja.cancel` is a sixth tool. The golden files in
  `internal/mcp/testdata/tools/` change with each slice and gain
  `forja.cancel.json`. Clients of the blocking shape (the MCP tests, FuseOne's
  M3 work, a Claude Code session over stdio) move to polling.
- **The run's work no longer runs on the request's context.** The supervisor
  owns every run goroutine and its context; `lifecycle.run` takes the run's
  context instead of the call's. Tests that cancel the request to stop a run
  (`cmd/forjad/mcp_cancel_test.go`) become tests that `forja.cancel` stops it,
  plus a test that a dropped request after the claim leaves the run running.
- **The drain waits for runs, not for requests.** `http.Server.Shutdown`
  returns as soon as the short start calls end; `serve` must also wait for
  the supervisor under the same 195 s budget, or the process would exit with
  runs mid-teardown. The tests that count the teardown's calls
  (`mcp.CancelledRunRecords`) gain the cancel and the shutdown causes.
- **ADR 0052's acceptance criterion C1 changes meaning.** A client closing the
  connection during `forja.implement` now finds the run still running, and
  `forja.cancel` is what removes the workspace and the claim (criterion X1
  below). Pilot release gate G6 becomes "the worker cancels a run with
  `forja.cancel`". ADR 0052 is amended by this ADR's ratification, not edited
  silently.
- **New failure reasons** `cancelled`, `deadline`, `token_budget`; the
  metrics' list grows.
- **Migration 0003** adds a column, an index and a table. It is additive, so
  a binary from before it keeps writing while it drains, as with 0002.
- **One replica is a precondition of `forja.cancel`.** A cancel reaches only
  runs of the process that answers it. Several replicas over one database
  need the cancel relayed through the store, and the run to record which
  process holds it (roadmap, open questions).
- **Per-process admission.** `FORJA_MAX_RUNS` bounds one process. Several
  replicas each have their own.
- **The model proxy's cap becomes per run.** The run's cap is the lower of
  `FORJA_MODEL_TOKEN_CAP` and the caller's `budget.max_tokens`, set when the
  run's proxy starts.
- **#60 is unaffected and composes.** `RunStarted.supersedes` and the held
  claim follow ADR 0052 §4's rendering, and `forja.cancel` uses the same
  owner-scoped read as `forja.status`.

## Acceptance criteria

Each is observable from outside `forjad`: an MCP client over HTTP against a
real listener, the run store in a real Postgres container, a real Docker
daemon, and a fake GitHub that enforces the API's invariants.

| # | Scenario | Observable result |
|---|---|---|
| S1 | `forja.implement` on a free issue | The call returns `RunStarted` with `state: running` before the agent stage starts; `refs/forja/claims/<n>` exists; `forja.status` with the `run_id` shows the stages as they progress, and the run's end |
| S2 | The client closes the connection after S1's answer, reconnects | `forja.status` (new connection, same client) shows the run still `running`, then terminal; the workspace was not torn down by the disconnect |
| S3 | A refused start (held claim, contract that does not parse, correction round on a branch Forja did not open) | A tool error; `forja.status` lists no new run; no key is stored (a later call with the same key, once the cause is gone, starts a run) |
| S4 | `forjad` killed (SIGKILL) with `FORJA_STATE_DIR` set while S1's run is in its agent stage, then started again | After the restart `forja.status` shows the run `failed`, reason `interrupted`; the claim ref is gone. Removing the dead run's containers is #84's, not this ADR's |
| I1 | `forja.implement` twice with the same key and arguments | The same `run_id`, the second with `replayed: true`; the repository has one claim commit for the issue |
| I2 | I1's second call after a `forjad` restart | The same `run_id`, `replayed: true`, in the run's terminal state |
| I3 | The same key with a different `finding` | Tool error `idempotency_key_conflict` naming the first `run_id`; no run recorded; no claim commit |
| I4 | Two clients, the same key, the same issue | One run for the first; for the second a held-claim error without the first's `run_id` (ADR 0052 §4, P3) |
| I5 | Implementation then correction: `K1` first run, `K2` correction, both with one `caller_ref` | Two runs; `forja.status` filtered by that `caller_ref` lists both; a retry with `K2` returns the correction's run; the correction with `K1` is `idempotency_key_conflict` |
| I6 | Two concurrent calls with the same key and arguments, in each order (the loser refused by the held claim; the loser claiming after the winner's run ended) | One run; the other call answers the replay, or a held claim naming the caller's own `run_id`; any claim commit of the loser is deleted within the release bound |
| I7 | The same key used for `forja.implement` and then `forja.verify` | `idempotency_key_conflict` |
| I8 | The same key and arguments with a different `caller_ref` | The replay; `RunStarted.caller_ref` is the first call's |
| X1 | `forja.cancel` on the caller's running run, delivered during its agent stage | `outcome: cancelling`; within 180 s `forja.status` shows `failed`, reason `cancelled`, `cancel_requested_at` set, no stage started after it; the claim ref is deleted; no container or network with the run's label remains; no commit status was published on the run's head |
| X2 | `forja.cancel` twice on the same run, and once on a succeeded run | The second answers `cancelling` (or `already_terminal` once ended); the succeeded run answers `already_terminal` and stays `succeeded` |
| X3 | Client B cancels client A's run | The tool error is identical to `forja.cancel`'s for a random id; A's run is unaffected |
| X4 | `forja.cancel` on a run recorded `running` that this process does not hold (a row a dead process left) | `outcome: not_held`; the record is unchanged |
| X5 | `forja.cancel` after the run's deadline fired, while it tears down | `outcome: ending`; the run ends `failed`, reason `deadline` or `wall_clock` |
| X6 | `forja.cancel` between the start call's first record and its goroutine's start (the start call held there by a test hook) | `outcome: cancelling`; no stage after the claim starts; the run ends `failed`, reason `cancelled` |
| X7 | `forja.cancel` during the release stage of a run whose work delivered | `outcome: ending`; the run ends `succeeded` |
| X8 | `forja.cancel` during the status stage, whose publish then succeeds | `outcome: cancelling`; the run ends `failed`, reason `cancelled`, though `forja/gates` was published |
| I9 | Two concurrent calls with one key on the same issue, one a first run and one its correction round | One run; the other call answers `idempotency_key_conflict` naming it, once the key is stored, or the held claim before |
| G2 | A stop while a start call is between its registration and its first record | The process exits only after that run's claim is released and its run recorded `failed`, `interrupted` |
| D1 | `deadline` five minutes ahead, `limits.wall_clock` 30 minutes | `limits.deadline` in `RunStarted` and in `forja.status` is the caller's; a run still working at it ends `failed`, reason `deadline` |
| D2 | `deadline` in the past | Tool error `deadline_passed`; no claim commit |
| D3 | `budget.max_tokens` below `FORJA_MODEL_TOKEN_CAP` | `limits.max_tokens` is the caller's; a run that reaches it ends `failed`, reason `token_budget` |
| D4 | `budget.max_tokens` above `FORJA_MODEL_TOKEN_CAP` | `limits.max_tokens` is the operator's; reaching it is `token_cap` |
| T1 | `forja.status` without `include_untrusted` on a run with red gates, egress refusals and proof paths | No `untrusted` key; no `stdout`, `stderr`, path, destination or evidence anywhere in the response; the counts and exit codes are present |
| T2 | The same with `include_untrusted: true` | `untrusted` carries the logs, paths, destinations and evidence; the structured fields are byte-identical to T1's |
| T3 | `RunStarted` from implement and verify | Contains no field of `untrusted` |
| B1 | `FORJA_MAX_RUNS=1`, one run in flight, a second `forja.implement` on another issue | Tool error `at_capacity`; no claim commit, no run, no key |
| B2 | B1's second call retried with the same key after the first run ended | A new run starts; no `idempotency_key_conflict` |
| B3 | B1's state, a replay of the first run's key | The replay answers; capacity does not apply |
| B4 | `FORJA_MAX_RUNS=1`, two starts refused after admission in a row (a held claim, a contract that does not parse), then a start on a free issue | The third starts a run: refused starts gave their slot back |
| R1 | A retry of a started run, with the same key and arguments, after its `deadline` has passed | The replay answers (`replayed: true`), not `deadline_passed` |
| R2 | `forja.implement` with `budget: {}`, then the same key without `budget` | The same `run_id`, `replayed: true` |
| P1 | `MCP-Protocol-Version: 2025-11-25` on any request | `400`, as ADR 0052 C3 |
| G1 | `forjad` stopped (SIGTERM) with two runs in flight | Both end `failed`, reason `interrupted`, claims deleted, workspaces removed, before the process exits within the 195 s drain |

## Open questions for the maintainer

Each has a recommendation written into the Decision above; ratifying this ADR
ratifies the recommendation unless the maintainer changes it.

1. **`caller_ref` shape.** Recommended: one opaque string (§3). The
   alternative is a structured pair `{work_item, caller_run}` matching PRD
   item 2's wording, which lets `forja.status` filter on the work item alone
   across FuseOne runs. Decide with FuseOne before v1.0.0; a string is
   cheaper to keep compatible.
2. **Run reasons, not states.** Recommended: `cancelled` is a failure reason
   of the state `failed` (§2). Confirm, or make it a fifth state.
3. **HTTP without a durable store or a state directory.** Recommended: once
   M2 lands, `forjad` refuses to start with `FORJA_MCP_ADDR` set and either
   `FORJA_DATABASE_URL` or `FORJA_STATE_DIR` unset. Asynchronous runs over
   HTTP are followed across reconnects and restarts, so the key must outlive
   the process, and without a journal a run that died with the process stays
   `running` forever (§1). stdio keeps the in-memory store and an optional
   state directory, where idempotency holds for the life of the process.
4. **Money budget.** Recommended: none in M2; the caller sends tokens and a
   deadline (§4). Revisit if FuseOne cannot convert its per-run budget.
5. **Default for `FORJA_MAX_RUNS`.** Recommended: unset means unlimited and
   the chart sets the number for the pool (§6). The alternative is a
   conservative default (for example 4) so a forgotten setting cannot
   overcommit the Docker host. Also: per-client quotas, so one client cannot
   take every slot, are not in this ADR; say if the pilot needs them.
6. **Status split by argument or by tool.** Recommended: one `untrusted`
   object behind `include_untrusted` (§5). If FuseOne's taint policy is per
   tool rather than per call argument, the same object moves to a read tool
   of its own (for example `forja.logs`), which keeps `forja.status` clean by
   construction.
7. **Protocol versions.** Recommended: HTTP stays on 2026-07-28 only (§8).
   Confirm; revisit when `forja.doctor` stops blocking or a client needs
   another version.
8. **`forja.doctor` stays synchronous.** Recommended (§1). The alternative is
   to make it asynchronous like the others, which removes the last blocking
   call and the last reason for §8.
9. **No resume after a restart.** Recommended: a run in flight at a stop ends
   `interrupted` and the caller starts a new one with a new key (§1). This is
   how this ADR reads the roadmap's "reads the same run to its end"; confirm
   that reading.
10. **Amending ADR 0052.** Ratifying this ADR replaces ADR 0052's C1 and G6
    with this ADR's X1 (Consequences). Confirm, and say whether ADR 0052
    should carry a note pointing here.

## What only the human can do

- [ ] Ratify this ADR, deciding or confirming each open question, and change
      its status to `accepted`.
- [ ] Agree the `caller_ref` shape and the error codes of §7 with the
      fuseone-agents maintainers, since FuseOne's M3 work consumes them.
- [ ] Confirm with FuseOne whether its taint policy can trust a field of an
      MCP response or only a whole tool's output (Open question 6).

## References

- [ADR 0033](0033-reservation-by-ref-with-lease.md), [ADR 0043](0043-run-limits.md),
  [ADR 0045](0045-run-cost.md), [ADR 0049](0049-operator-view.md),
  [ADR 0051](0051-forja-as-a-separate-product.md),
  [ADR 0052](0052-network-transport-and-caller-authentication.md),
  [ADR 0076](0076-durable-run-store.md).
- [Roadmap](../roadmap.md), "How FuseOne reaches an ephemeral Forja" items 4
  to 6, M2 and open questions; [PRD](../PRD.md), "Alignment with the FuseOne
  factory (NT-013)", items 2 to 5.
- Code at `1e482a2`: `internal/mcp/implement.go`, `verify.go`, `lifecycle.go`,
  `implement_stages.go`, `implement_agent.go`, `implement_deliver.go`,
  `implement_round.go`, `status.go`, `status_owned.go`, `tools.go`,
  `doctor.go`, `testdata/tools/`; `internal/runstore/postgres.go`,
  `memory.go`, `migrations/`; `internal/claim/mutation.go`;
  `internal/domain/run.go`, `claim.go`, `contract.go`, `usage.go`,
  `metrics.go`; `cmd/forjad/main.go`, `mcp_http.go`, `mcp_protocol.go`,
  `agent.go`, `recover.go`.
