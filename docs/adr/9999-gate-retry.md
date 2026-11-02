# ADR 9999: a red gate goes back to the agent up to the attempt limit

- **Status:** proposed (default taken by the team; awaits human ratification)
- **Issue:** #17
- **Refines:** [ADR 0042](0042-implement-delivery.md) §4, §7 and §9, and
  [ADR 0038](0038-model-proxy.md) §1 and §4. It supersedes neither.
- **Date:** 2026-11-02

## Context

PRD, "Fluxo de forja.implement", step 6: a red gate goes back to the agent up
to the attempt limit; once it is exhausted, the run ends as a failure with the
evidence. Issue #17 sets the default limit at 3, configurable, and has every
attempt draw on the same token cap and the same wall clock.

Before this decision `forja.implement` ran the agent, the gates, the push, the
pull request and the status once. ADR 0042 §7 ended a red gate with
`gate_failed` and nothing went back to the agent. ADR 0038 §1 gives each agent
launch its own model proxy, capped at `FORJA_MODEL_TOKEN_CAP`, so a second
launch of the same run would have had the whole cap again.

## Decision

### 1. The limit lives in the contract

`.forja.yaml` v1 gains `limits.max_attempts`, a positive integer next to
`wall_clock`. A zero, a negative or a non-integer is a contract defect at the
key's line. Omitted, `domain.Limits.MaxAttempts` stays 0 and
`Limits.Attempts()` reads it as `domain.DefaultMaxAttempts`, 3, as `LeaseFor`
defaults an omitted wall clock. The limit is read from the base commit's
contract in `prepare`, like every other field: an agent that raises it in its
own tree changes nothing. The pilot's `.forja.yaml` does not set it.

### 2. One attempt is a fresh agent workspace, then a fresh gates workspace

ADR 0042 §4 holds on every attempt: the agent workspace and its model proxy
are gone before the gates run. Attempt n ≥ 2 launches a new agent workspace on
the control plane's clone, which after `Adopt` holds the previous head checked
out on `forja/<n>` with its `.git`. The agent commits on top of its earlier
commits. The export stays `<base>..forja/<n>`, so each bundle carries every
commit of the run. Each attempt's bundle has its own path in the run's bundle
directory, because the export refuses a destination that already exists.

### 3. The same token cap across launches

`FORJA_MODEL_TOKEN_CAP` stays the run's total. A launch now carries `Spent`,
the run's `Tokens.Total()` from the earlier attempts, and its proxy gets
`-cap <cap − spent>`. A launch whose run already spent the cap, or more, is
refused with `workspace.ErrCapSpent` before anything of the run exists. The
adapter in `cmd/forjad` turns it into `agent.ErrCapSpent`, and `agent.Runner`
reads it as `Result{Capped: true}` with no error, as it reads a cap reached
mid-run. The agent package does not import the workspace package.
`agent.Launcher.LaunchAgent` and `workspace.Docker.LaunchAgent` take an options
struct (`agent.Launch`, `workspace.AgentLaunch`), field for field the same.

The run's tokens and egress add up across attempts. The registry report merges
like the egress report, so every gates workspace adds to it.

### 4. What goes back to the agent

The next attempt's prompt gains a section with the attempt number out of the
limit and the step `GateReport.FailedStep` names, a gate or `setup[i]`: its
command, status, exit code (`none` with the control plane's reason when the
step produced none) and both raw streams exactly as the gates runner kept
them, up to `workspace.MaxStreamBytes` (64 KiB) each. Each stream is quoted in
a fence longer than any run of backticks inside it. The section ends telling
the agent to fix the failure by adding commits on the same branch. The streams
are neither read nor logged.

### 5. The loop

For attempt n from 1 to the limit, the agent stage and then the gates stage
run:

| Attempt ends | Then |
|---|---|
| agent capped, timed out, exited non-zero or failed to run | the run stops as before, no retry |
| attempt 1 with no commit | `no_commits`, as before |
| attempt n ≥ 2 whose `Adopt` returns the head the gates already ran on | `no_commits`, without running the gates again |
| gates green | push, pull request and status, as before |
| gates red, n below the limit, the run's deadline not passed | attempt n + 1 |
| gates red otherwise | `failed`, `gate_failed`, `failed_step` of the last attempt; no push, pull request or status |

Only the control plane decides a retry, and only on `GateReport.OK()`, the exit
codes. The deadline is the single run context of ADR 0042 §9; no attempt
starts once it passed. The claim, the release and the terminal record are
unchanged: every attempt runs under the one claim. The remote `forja/<n>` is
pushed once, after the last attempt is green, never forced. The clone handed
to a later agent was never pushed from.

### 6. Adopt across attempts

`Checkout.Adopt` fetches into `refs/forja/adopted` with a forced refspec,
`+refs/heads/forja/<n>:refs/forja/adopted`. An agent that amended or rebased
an earlier attempt's commit hands back a head that does not descend from the
one adopted before; git refused that update without the `+`. The ref is the
control plane's own. The bundle verification, the head differing from the
base, the base being the head's ancestor and the disabled hooks all stay.
A rewrite between attempts never reaches GitHub, since nothing is pushed
before the last attempt.

### 7. The record and the wire

`domain.Run` gains `Attempts []Attempt` (`Number`, `HeadSHA`, `Gates`,
`Tokens`, appended when an attempt's gates ran) and `MaxAttempts`, set from
the base contract. `Run.Gates` and `Run.HeadSHA` stay the latest attempt's, so
`setup[]`, `gates[]` and `head_sha` keep their meaning. `domain.Stage` gains
`Attempt`, 0 for the claim, clone, push, pull request, status and release.

`forja.status` and `forja.implement` gain `attempt` (the attempt in flight or
the last one, 0 before the first agent stage), `max_attempts` (0 until the
contract is read) and `attempts[]` (`{number, head_sha?, failed_step?,
tokens_in, tokens_out, setup[], gates[]}`, always on the wire). Each stage
carries `attempt`, absent outside the attempts.

### 8. Evidence

The header gains "Attempts | k of max". The last attempt's steps and logs
render as before. Earlier attempts follow under "Earlier attempts", each with
its head, failed step, tokens and steps table, then its logs. Under the 60 000
byte body limit every table and the last attempt's logs are kept first; the
earlier attempts' logs are the first to be dropped, whole blocks only, with
the existing truncation note. The footer is always kept, and the renderer
still interprets nothing.

## Measurements

Local git 2.54.0 (Apple Git-157), local Docker 29.5.2 on colima, linux/arm64.

| Question | Result |
|---|---|
| `TestAdopt_secondBundleRewritesFirstCommit_adopted`, real git: attempt 2 amends attempt 1's commit, the non-forced refspec | `git fetch` exits 1, the rewritten head is not adopted |
| The same test with `+refs/heads/forja/<n>:refs/forja/adopted` | the rewritten head is adopted and checked out; it still descends from the base |
| `TestAdopt_secondBundleSameHead_returnsSameHead`: attempt 2 adds nothing | `Adopt` returns attempt 1's head again, no error |
| `TestDocker_launchAgent_spentLowersProxyCap`, real Engine: cap 10, spent 8, a request asking `max_tokens` 3 | 429 with `forja: run token cap reached` on the first request |
| The same run id relaunched after `Close` with nothing spent, same request | 200: the remainder decided it, and a second agent launch of one run does not collide with the first |
| `TestDocker_launchAgent_capSpent_refusedNothingLabelled`: spent 10 and 11 of 10 | `ErrCapSpent`, no container or network labelled with the run |

A cap of 10 with 9 spent cannot tell the remainder from the full cap with the
test's fake upstream: its first response carries about 137 tokens, so either
cap is reached on the first request. The test asks for more `max_tokens` than
the remainder instead, which the proxy refuses before forwarding.

## Consequences

- Input can overshoot the cap by up to one request's input per attempt: each
  attempt's proxy is new and admits a request whose `max_tokens` fits its
  remainder, whatever that request's input (ADR 0038 §4).
- A remainder below the agent's `max_tokens` (128 000 for Claude Code) caps the
  attempt on its first request. A cap that is to cover the default 3 attempts
  must leave at least that much for the last one.
- Each attempt pays its own agent workspace and its own gates workspace, setup
  included.
- `attempts[]` lists the attempts whose gates ran. An attempt that stopped at
  the agent stage shows in `stages[]` with its number, and its tokens are in
  the run's totals, not in `attempts[]`.
- The run record keeps every attempt's gate report, so it grows with the
  attempts by up to two kept streams per step.
- A failed run carries every attempt in `evidence` and `attempts[]`, but opens
  no pull request.

## What only the human can do

- [ ] Ratify `limits.max_attempts` as a v1 contract key with a default of 3,
      the cap's remainder per launch and the forced update of
      `refs/forja/adopted`. On ratification, change this ADR's status to
      `accepted`.
- [ ] Size `FORJA_MODEL_TOKEN_CAP` for the attempts a run may take.

## References

- `docs/PRD.pdf`, "Fluxo de forja.implement", step 6.
- [ADR 0042](0042-implement-delivery.md), [ADR 0038](0038-model-proxy.md).
- `CLAUDE.md`, rules 2, 3, 4, 5 and 6.
