# ADR 0049: the platform owner's view of runs per repository

- **Status:** proposed (default taken by the team; awaits human ratification)
- **Issue:** #24
- **Refines:** [ADR 0045](0045-run-cost.md), whose per-run compute, tokens
  and estimate this adds up per repository. It supersedes nothing.
- **Date:** 2026-11-02

## Context

In the PRD (`docs/PRD.md`) the platform owner's "Operate and charge" use case
"sees runs, duration, compute cost and failures per repository". Before this
decision `forja.status` answered one run by id, `run_id` was required, the run
store could not enumerate, nothing added runs up, and `forjad serve` spoke
stdio only.

FuseOne ingests no runtime metrics (measured on `fuseone-agents` at
`origin/main` 900349de). Its run and failure panel is a projection of its
own ledger: it has no Prometheus or OpenTelemetry library, no scrape, OTLP
receiver or push path, and it never calls `forja.status`; what it sees of
Forja is the `forja.implement` tool-call outcome. Its own convention for
exposing metrics is hand-written Prometheus text, format 0.0.4, on an opt-in
listener that an empty address disables, with labels drawn from fixed
vocabularies and never a run id. Forja's metrics are therefore for the
operator's Prometheus, in FuseOne's own style.

## Decision

### 1. One fold, two readers

Every figure comes from the run store the control plane writes; nothing is
read from a workspace. `domain.Summarize` folds runs into one total per
repository, and both `forja.status` and the metrics exposition use it, so
the tool and the panel cannot disagree. A failure is counted under the
control plane's `Run.Reason`, never under parsed output. A failed run with no
reason, or one outside the known set, is counted under `unspecified`, a key of
the metrics only: `Run.Reason` gains no value.

### 2. `forja.status` without an id lists runs

Every argument is optional:

| Argument | Meaning |
|---|---|
| `run_id` | one run, as before; with any other argument it is an error naming that argument |
| `repository` | exact `owner/name` |
| `tool`, `state` | a known tool or run state; any other value is an error |
| `since`, `until` | RFC3339 instants on `started_at`, `since` inclusive, `until` exclusive; `since` must be before `until` |
| `limit` | runs per page, 1 to 200; omitted or 0 is 50; anything else is an error |
| `cursor` | the `next_cursor` of the previous page; an unknown run id is an error |

Runs come newest first: by `started_at` descending, then by id descending,
so no two runs tie. The cursor is the id of the page's last run, and the next
page is keyed on that run's start and id, not on an offset: a run recorded
between two pages does not shift the walk. Every invalid input is a tool
error, never a protocol error.

### 3. The `list` envelope

go-sdk v1.8.0 validates the structured output against the schema it infers
from the output type, and jsonschema-go v0.4.3 makes every field without
`omitempty` required. A union of the by-id shape and the list shape, embedded
or as `oneOf`, would therefore fail validation on one of the two answers.
`StatusOutput` gains one field instead, `list`, absent on a call by id: the
by-id wire is unchanged, key for key. Without an id the per-run fields are
their zero values and `list` carries the answer:

- `runs`, always an array: `id`, `tool`, `repository`, `issue`, `state`,
  `started_at`, `finished_at` (absent while in flight), `failure_reason`
  (absent when none), `tokens_in`, `tokens_out`, `compute_minutes`,
  `estimated_cost_usd_micros` (absent without a price table) and
  `egress_denied_count`. Stages, gate logs and commits stay by id.
- `next_cursor`, absent on the last page.
- `repositories`, always an array, sorted by repository, over every run the
  filter matches, not only the page, and repeated on every page: `runs`,
  `succeeded`, `failed`, `in_flight` (queued and running),
  `average_duration_seconds` over the finished runs to three decimals and
  absent when none finished, `compute_minutes` summed then rounded once,
  `tokens_in`, `tokens_out`, `estimated_cost_usd_micros`, `egress_denied_count`,
  `egress_denied` and `failures`.
- `estimated_cost_usd_micros` of a repository is the sum of its runs' own
  estimates, each floored as `forja.status` answers it, so it equals what
  FuseOne gets adding the runs up; it is absent without a price table.
- `egress_denied` lists `{run_id, at, kind, destination}` in the list's order,
  the first 100; `egress_denied_count` is every attempt.
- `failures` has seven rows, always all seven, in this order: `gate_failed`,
  `token_cap`, `wall_clock`, `diff_too_large`, `no_commits`,
  `inversion_unavailable`, `unspecified`.
  Each row is `{reason, count, rate}`, where `rate` is `count` over the
  finished runs, succeeded plus failed, to four decimals, and 0 when none
  finished.

### 4. The metrics

`forjad serve` reads `FORJA_METRICS_ADDR`, a `host:port`. Unset, it opens no
listener. A value that is not `host:port`, or that cannot be bound, stops
`serve` with exit 1 and an error naming the variable. Set, `serve` listens
before it runs the MCP server, serves `GET /metrics`, and closes the listener
when the MCP server returns, whether the transport closed or the context
ended.

The exposition is Prometheus text format 0.0.4, written with the standard
library and folded from the run store at each scrape. Every metric is a gauge,
since the history resets with the process:

| Metric | Labels |
|---|---|
| `forja_runs` | `repository`, `state` = `succeeded`, `failed` or `in_flight` |
| `forja_run_failures` | `repository`, `reason`, the seven above |
| `forja_run_duration_seconds_sum`, `forja_run_duration_seconds_count` | `repository`; the wall time and the number of finished runs |
| `forja_compute_seconds` | `repository` |
| `forja_tokens` | `repository`, `direction` = `in` or `out` |
| `forja_egress_denied` | `repository` |
| `forja_estimated_cost_usd_micros` | `repository`; only when the server has a price table |

Label values are escaped as the format requires. No run id, issue, gate log
or destination becomes a label.

### 5. Exposure

The listener has no authentication and no TLS in this decision. It hands out
totals per repository and no credential, and nothing a workspace could use
to write a status. The operator binds it to the loopback interface, or to a
private interface of the control plane that their Prometheus reaches. A
workspace sits on its `--internal` network
([ADR 0026](0026-workspace-isolation.md), [ADR 0035](0035-egress-gateway.md)),
which has no route out, and its gateway dials public addresses only. On a
Linux host that runs workspaces, however, the host holds an address on each
run's bridge, and a listener on the wildcard address would answer there. That
reachability was not measured for this decision, so on a host that runs
workspaces the listener never binds the wildcard address. forjad does not
enforce this.

### 6. History

The run store is in memory. Every list, total and metric covers the runs the
current process recorded, and resets when it restarts.

## Consequences

- The platform owner reads runs, duration, compute, tokens, cost, failure
  rate by reason and denied egress per repository, from the tool or from
  Prometheus.
- A call to `forja.status` without `run_id` no longer fails at the input
  schema: it lists.
- `repository` is a label, and its values are as many as the repositories
  the server ran. FuseOne forbids unbounded labels; one Forja serves the
  repositories of its areas, a bounded set, but a server spread over a very
  large number of repositories raises the cardinality accordingly.
- A restart empties the history; Prometheus sees every gauge drop.

## What only the human can do

- [ ] Ratify the `forja.status` list arguments, the `list` envelope and its
      fields, and the metric names and labels. On ratification, change this
      ADR's status to `accepted`.
- [ ] Decide where `FORJA_METRICS_ADDR` binds on the deployed control plane,
      and which Prometheus scrapes it.
- [ ] Decide whether the run history must survive a restart, which needs a
      persistent run store.

## References

- `docs/PRD.md`, the "Operate and charge" use case.
- [ADR 0045](0045-run-cost.md), [ADR 0026](0026-workspace-isolation.md),
  [ADR 0035](0035-egress-gateway.md).
- `CLAUDE.md`, rules 3, 4 and 5, and "No personal data in logs".
