# ADR 0033 — Issue reservation by ref with lease

- **Status:** proposed
- **Date:** 2026-11-01
- **Issue:** #12
- **Measured base:** `origin/main` at `a058ad9433598d3699e1eae575066af4ff92453f`

## Context

The PRD asks for one run per issue: `forja.implement` reserves the issue in
`refs/forja/claims/<n>` before any work and refuses when the ref exists; the
reservation has a lease that expires and gives the card back; the run
releases it at the end. `CLAUDE.md` rule 6 adds the crash case: no claim left
behind after the run, whatever ended it.

Forja has no database of its own in phase 0 (the run store is in memory) and
runs as a single process. Whatever makes the reservation atomic has to live in
the repository, reachable with the GitHub App token the control plane already
holds, and has to survive the process dying.

## Decision

### The claim is a ref pointing at a parentless commit

`refs/forja/claims/<n>` points at a commit whose tree holds one file,
`claim.json`:

```json
{
  "version": 1,
  "run_id": "…",
  "repository": "owner/name",
  "issue": 12,
  "created_at": "2026-11-01T12:00:00Z",
  "expires_at": "2026-11-01T12:45:00Z",
  "supersedes": { "run_id": "…", "sha": "…", "expires_at": "…" }
}
```

The commit has no parent: it is not part of any branch's history and never
reaches a PR. `supersedes` is present only when the claim took over an expired
one.

Creating the ref is the reservation. The Git Data API creates a ref only if it
does not exist, so two runs racing for the same issue cannot both succeed; the
loser reads `claim.json` at the ref and learns the holder's run id, which is
what the tool error carries back.

### Taking over an expired lease is a fast-forward-only update

When the ref exists and `expires_at` is in the past, the new run writes a
child commit of the holder's claim commit, with `supersedes` filled, and moves
the ref onto it with `force: false`. The API refuses a move to a commit that
does not descend from the ref's current value, so if another run moved the ref
first the update fails and the new holder is read and reported. The ref is a
compare-and-swap; nothing in this slice ever sends `force: true`.

A claim that exists but cannot be decoded (not JSON, another `version`, no
run id, no expiry, or describing another issue) is refused and never taken
over. Taking over what one does not understand is how two runs end up on one
issue.

### Release is verify-then-delete

The run reads the ref, checks it still points at its own claim commit and
only then deletes it. A ref that moved on belongs to another run and is left
alone; the releasing run ends `failed` with `ErrClaimLost`, because its lease
was taken over and its work may have been duplicated.

The window between the check and the delete is accepted. For it to matter, the
lease would have to expire between the two calls, and the lease is by
construction longer than the run's wall clock: a run that reaches its release
still holds an unexpired lease. The GraphQL `updateRefs` mutation with
`beforeOid` would close the window with a real compare-and-swap on delete; it
is the upgrade path if the window ever stops being theoretical.

### Crash recovery is a local journal

The only state that survives a crash is a journal under
`FORJA_STATE_DIR/claims/`: one file per held claim,
`<owner>__<name>__<issue>.json`, written to a temporary name and renamed. On
start, before the server accepts a call, every journaled ref that still points
at the crashed run's claim commit is deleted; one that moved on or vanished is
another run's and only the entry is forgotten. A journal that cannot be read
is a startup failure.

Without `FORJA_STATE_DIR` the server serves and says once on stderr that a
claim left by a crash is freed only by its lease. The journal is per process:
two instances with separate state directories do not recover each other's
claims. Multi-instance recovery arrives with a shared run store, not here.

> Note (ADR 9999): "per process" is now enforced, not assumed. `serve` holds
> an exclusive `flock` on `FORJA_STATE_DIR/forjad.lock` for its life and
> refuses to start, before reading the journal, when another process holds
> it.

### Lease: `wall_clock + 15m`, `60m` without limits, configurable upwards

The lease is sized from the contract read at the base commit (rule 2, SHA
pinned in the run): `limits.wall_clock` plus a 15-minute margin, or 60 minutes
when the contract sets no limits. `FORJA_CLAIM_LEASE` overrides it but must
exceed `wall_clock`; a shorter value is a startup or tool error, never a
shorter lease. The margin is what makes a run still working impossible to
take over and what keeps the release window theoretical.

### `contents:write`, minted per call

Creating refs needs `contents: write`; reading the contract needs `read`. The
token source now takes the permissions of each call: `ContractReader` asks
for read, `Refs` asks for write, and the App mints exactly that for the one
repository. Nothing wider is held than the call needs, and no token is cached.
The App's permission table in `docs/github-app.md` already lists
`contents: write`; it does not widen.

## Consequences

- Two simultaneous `forja.implement` calls for one issue: one reserves, the
  other is refused with the first one's run id. Verified against an in-memory
  fake that enforces the API's create-if-absent and fast-forward-only rules,
  under the race detector, and against an httptest fake with the API's status
  codes.
- The ref is removed on success, on failure and on the next start after a
  crash. `forja.status` shows `supersedes` when a run took over an expired
  lease.
- Every refused attempt and every take-over leaves blob, tree and commit
  objects in the repository that no ref points at. GitHub exposes no API to
  delete objects; they are garbage-collected by the server. "Leave nothing
  behind" holds for the ref.
- The fake's `422` error messages (`Reference already exists`, `Update is not
  a fast forward`, `Reference does not exist`) are the API's documented status
  codes with illustrative messages: the write side was not measured against
  the pilot because the measurer role is read-only. `Refs` decides on the
  status code alone and never on the message. A probe script under
  `scratchpad/team/12/probe-claims-ref.sh` is ready to confirm the bodies
  under a writer.

## Out of this decision

Everything between claim and release (clone, workspace, agent, gates, push,
PR, `forja/gates`), honouring `base_branch` in the contract read, enforcing
`wall_clock` as a run deadline, the persistent run store and multi-instance
recovery, and `forja.plan`/`forja.verify`.
