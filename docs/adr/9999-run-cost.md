# ADR 9999: compute minutes, tokens and estimated cost per run

- **Status:** proposed (default taken by the team; awaits human ratification)
- **Issue:** #14
- **Refines:** [ADR 0038](0038-model-proxy.md), which left the USD figure and
  the tokens of a run in flight to #14, and [ADR 0042](0042-implement-delivery.md),
  whose `forja.implement` and `forja.status` wire this extends. It supersedes
  neither.
- **Date:** 2026-11-02

## Context

In the PRD (`docs/PRD.md`) the platform owner's "Operate and charge" use case
"sees runs, duration, compute cost and failures per repository". "Forja
reports compute minutes and agent tokens per run; FuseOne adds them up per
area" and applies the per-run cap it already has. `forja.status` is "state,
log and cost of a run". The risk "Compute cost with no owner" is answered by a
"per-run report to FuseOne".

Before this decision both tools already returned `tokens_in` and `tokens_out`,
read from the model proxy's log on every outcome. Nothing measured how long
the workspaces and their sidecars were up, nothing priced a run, stages carried
only their times and outcome, and a gates runner that failed returned an empty
report, so its workspace's time was lost.

FuseOne defines no wire shape for a runtime's cost in a tool result (measured
on `fuseone-agents` at `origin/main` 900349de). The engine has a `Cost` slot
on `ToolResult` that the MCP invoker never fills. Its internal shapes count
money as integer `micros` and time as `wall_clock_ms`; none has compute
minutes. The shape below is therefore Forja's own.

## Decision

### 1. What compute counts

Compute is container time, measured by the runner's injected clock, the
control plane's. Nothing the workspace writes or reports enters it.

| Container | From | Until |
|---|---|---|
| Agent workspace | just before `LaunchAgent` | `Close` returned; a failed launch, until it returned |
| Gates workspace | just before `Launch` | `Close` returned; a failed launch, until it returned |
| Each contract service sidecar | just before its `StartService` | the gates workspace's `Close` returned |

A sidecar is counted once per run, as it is started once per run; one whose
start failed is counted from its attempt, like a failed launch. Forja's own
per-run containers, the egress gateway, the model proxy and the registry proxy,
and the control plane's clone are not counted: they are Forja's overhead, not
the area's workload.

The agent stage's compute is the agent workspace's, the gates stage's is the
gates workspace's plus its sidecars', and the run's is the sum of its stages'.
A `forja.doctor` run records no stages; its compute is its gates workspace's.
The gates runner returns the compute with the error when it fails, and the run
is charged it before the error is returned.

forjad gives both runners `time.Now`, not the UTC clock the run is stamped
with: `UTC()` strips the monotonic reading the difference needs.

### 2. The price table

The prices are operator configuration on the control plane, read from the
environment by forjad. Each is a non-negative integer:

| Variable | Unit |
|---|---|
| `FORJA_PRICE_INPUT` | USD micros per million uncached input tokens |
| `FORJA_PRICE_CACHE_WRITE` | USD micros per million cache creation tokens |
| `FORJA_PRICE_CACHE_READ` | USD micros per million cache read tokens |
| `FORJA_PRICE_OUTPUT` | USD micros per million output tokens |
| `FORJA_PRICE_COMPUTE_MINUTE` | USD micros per container-minute |

All five or none. A partial or malformed set stops the server at start with an
error naming the variable: a table missing a price would bill that class as
free. With none set the server has no table, and the estimate is absent from
the wire, never 0. No price is hardcoded in the source, so Forja asserts no
vendor's price.

### 3. The estimate

`domain.Cost` is USD micros in an `int64`; no float holds money.
`PriceTable.Estimate(compute, tokens)` is pure:

```
tokens term  = floor((Input·pIn + CacheCreation·pCW + CacheRead·pCR + Output·pOut) / 1 000 000)
compute term = floor(compute_ms · pMinute / 60 000)
estimate     = tokens term + compute term
```

It is applied when a run is answered, by `forja.implement` and by
`forja.status`, with the server's current table. A run read after the operator
changed the prices is estimated at the new ones. The products stay inside
`int64` for token counts under 2^32 at prices under 10^8 micros (USD 100 per
million tokens or per minute).

### 4. The wire

Everything is added; nothing that existed changes.

- `forja.implement` and `forja.status` gain `compute_minutes`, a JSON number,
  the run's compute in container-minutes rounded to three decimals, always
  present and 0 when nothing ran, and `estimated_cost_usd_micros`, an integer
  absent when the server has no price table. The estimate prices the duration
  itself, not the rounded minutes.
- `tokens_in` (input plus both cache fields) and `tokens_out` stay as they
  are.
- Each `stages[]` entry of `forja.status` gains `compute_minutes`, `tokens_in`
  and `tokens_out`, always present, 0 for a stage that used none.

A run that fails or hits a limit reports its cost like any other. When the run
ends in a tool error, `forja.implement` answers no result, and the cost is on
the run `forja.status` reads.

### 5. A run in flight

A stage's compute and tokens are recorded when it ends. `forja.status` on a
running run reports what the finished stages recorded, so a run in its gates
stage already shows the agent stage's cost, and the stage that is running
contributes 0 until it ends. Metering a stage while it runs, which would mean
polling the model proxy's log and the containers, is not part of this
decision.

## Consequences

- FuseOne can add `compute_minutes`, `tokens_in`, `tokens_out` and
  `estimated_cost_usd_micros` up per area from either tool, failed and capped
  runs included.
- The estimate is only as right as the operator's table; Forja checks it is
  complete and non-negative, not that it matches any bill.
- A long agent stage shows 0 for its own cost until it ends.
- The PR body keeps its "Tokens in / out" and "Elapsed" rows; it carries no
  cost.
- Prices far above USD 100 per unit, or a run of more than 2^32 tokens of one
  class, can overflow the estimate; neither is refused.

## What only the human can do

- [ ] Ratify the five `FORJA_PRICE_*` variables, their units and the
      all-or-none rule, and set the table on the deployed control plane.
- [ ] Ratify that the egress gateway, the model proxy, the registry proxy and
      the clone are Forja's overhead and are not counted as compute. On
      ratification, change this ADR's status to `accepted`.
- [ ] Confirm with FuseOne's FinOps that the wire names and units above are
      what it will add up per area, since FuseOne defines no shape of its own.

## References

- `docs/PRD.md`, the "Operate and charge" use case, the tool table, "Cost"
  and the risk "Compute cost with no owner".
- [ADR 0038](0038-model-proxy.md), [ADR 0042](0042-implement-delivery.md).
- `CLAUDE.md`, rules 3, 4 and 6, and "Float for money" and "`time.Now()` inside
  business logic" under Forbidden.
