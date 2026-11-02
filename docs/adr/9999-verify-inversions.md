# ADR 9999: forja.verify inversions, the QA agent and the verdict "bites / does not bite"

- **Status:** proposed (default taken by the team; awaits human ratification)
- **Issue:** #22
- **Refines:** [ADR 0048](0048-verify-not-verifiable.md). It replaces the
  production row of 0048 §4: a diff with production code no longer fails
  with `inversion_unavailable` but is inverted. It supersedes nothing.
- **Date:** 2026-11-02

## Context

PRD, "forja.verify": the QA agent picks the inversions the way the pilot did.
It takes the half of the change a real regression would undo, then the
complement of what the developer said they tested. An inversion touches only
production code, the new tests stay, and Forja runs the base contract's gates
and requires red. Reverting the whole diff does not count. The verdict
**bites** when every inversion turned some gate red, and `forja/qa` is then
green. It **does not bite** when some inversion passed green, and `forja/qa`
is then red and names that inversion. The risk table says that verify with an
agent makes every PR more expensive, so QA cost is measured on its own.

Before this decision a diff with production code ended at stage `diff` with
`inversion_unavailable` (ADR 0048 §4). Nothing picked, applied or gated an
inversion.

## Decision

### 1. The tree is the pull request's head on the base clone

Every verify run clones with `BranchCloner.CloneBranch(repo, base, head ref)`.
Reads, and so the contract, still answer for the base commit. `Dir` is the
head, and `Start` is the head's tip. When `Start` differs from the `head.sha`
read before the claim, the head moved after the read. The clone stage then
fails with an error and publishes no status.

### 2. A baseline before the agent

Stage `gates` runs the base contract's gates on the head. A head that is not
green fails the run with `gate_failed`, with no verdict and no status. An
inversion of a red head proves nothing.

### 3. The QA agent

`mcp.QAAgent.RunQA(agent.QATask)` is served by the same `*agent.Runner` as the
implement agent, in stage `agent`. It shares the launcher, the model proxy,
the cap, the metering and the close: no credential is in its workspace.

The prompt goes through the same `agent.Command()`. It quotes the issue and the
pull request's body in fences they cannot close, as untrusted text. It tells
the agent to read `git log base..head` as the developer's declarations. It
lists the production paths, the only paths an inversion may touch, with
`git diff base head --`, and it lists the gates. It asks for up to
**4 candidates** in order of preference on `forja-qa/1`..`forja-qa/4`, each
started at the head. The first should be the half a real regression would
undo, then the complement of what was declared tested.

After an exit 0 each branch is exported from the head into its own bundle on
the control plane. A branch left at the head (`ErrNoCommits`) or never made
(`domain.ErrNoBranch`) is not exported, and that is a result. A capped run
exports nothing.

### 4. Admission, on the control plane

Each exported candidate is adopted into the clone. `Checkout.Changes` reads
what it changes against the head and against the base. It uses no rename
detection, so a rename counts both paths, and it takes attributes from the
base commit. The patch is bounded at 64 KiB. `verify.Admit` refuses a
candidate in four cases, checked in this order:

| Reason | When |
|---|---|
| `empty` | The candidate changes nothing against the head (`Adopt` says `ErrNoCommits`, or no path). |
| `touches_non_production` | A path is excluded or a proof path under the base contract, by the same fold as `verify.Classify` (exclude beats proof). On the pilot this is every `**/*_test.go` and `**/testdata/**`. |
| `outside_the_change` | A path is not among the pull request's production paths. |
| `reverts_whole_diff` | No production path of the pull request is still changed between the base and the candidate. |

The last row reads "the whole diff reverted" as "the whole production change
reverted". A candidate can never touch the new tests, so a diff from the base
that is literally empty only happens for a pull request without tests. The
rule as written covers that case too.

The first **2** admitted candidates, in order, run. Candidates after them are
never looked at. Rejected candidates stay in the run with their reasons. When
none is admitted, the run fails with `inversion_unavailable` and no status.

`Adopt` now fetches into its private ref with a forced refspec. A candidate
started at the head does not fast-forward the previous one, and git refused
the second import before this change. The descent check after the fetch still
decides what may be checked out.

### 5. Inversion gates and the fold

Each admitted candidate is one stage `inversion`. The stage adopts the
candidate again, so `Dir` holds that tree, then runs the base contract's gates
and charges their compute. Every admitted inversion runs; nothing stops early.

`verify.Judge` reads only step status and exit codes:

- **bit**: the first step that did not pass is a gate, not `setup[i]`, with
  status `failed` and an exit code;
- **escaped**: every step passed;
- **inconclusive**: anything else, such as a red setup step, a service that
  never became ready or a gate the deadline stopped.

`verify.Fold` gives **bites** when every inversion bit. It gives **does not
bite**, naming the first escaped branch, when all are conclusive and one
escaped. One inconclusive inversion fails the run with
`inversion_inconclusive` and no status. A run the wall clock ended reports
`wall_clock`, wherever it fired. The deadline carries the cause, as
implement's does.

### 6. forja/qa

| Verdict | State | Description |
|---|---|---|
| bites | `success` | Bites: every inversion turned a gate red. |
| does not bite | `failure` | Does not bite: inversion forja-qa/<k> passed every gate. |
| not verifiable | `pending` | ADR 0048's two fixed lines. |

Every description is under 140 characters, and any other verdict publishes
nothing. The control plane publishes, never the workspace.

### 7. Cost, wire and evidence

The QA cost is separate by construction. The verify run has its own `run_id`.
Its `agent` stage carries the QA tokens and compute, and its `gates` and
`inversion` stages carry container compute. Nothing is added to the implement
run.

`forja.verify` gains `compute_minutes`, `estimated_cost_usd_micros` (from the
operator's price table) and `inversions`, which is always on the wire and `[]`
for none. Each inversion has `branch`, `commit`, `paths`, `admitted`,
`rejected`, `outcome` and `failed_step`.

The evidence gains three things:

- a "Baseline gates on the head" row;
- a tokens and compute row for this run only;
- an "Inversions" section with each candidate's branch, commit, outcome or
  rejection, red gate, paths as code spans, and patch in a fence it cannot
  close.

The evidence is still returned, not posted. `forja.status` already shows the
verify run's stages, tokens and compute and is unchanged.

## Measurements

From the measurer, on origin/main `08d9719`.

| Question | Result |
|---|---|
| Body of a pull request with no description | `null`, never `""`. golang/tools, 100 closed pull requests: 5 `null`, 95 strings, 0 empty strings. `pulls/581`, `591` and `605` one by one: `"body":null`. The pilot has no empty-body pull request (24 of 24 are strings). |
| Pilot contract as-is in a fresh `golang:1.25` container | Red at `setup[1]` in 3 of 3 runs, in 3.4 to 4.3 s. `GOTOOLCHAIN=local` refuses golangci-lint v2.15.0, which needs go 1.26. |
| Pilot gates, cold workspace, with `GOTOOLCHAIN=auto`, `safe.directory` and readiness | All green: 73.0, 87.1 and 79.6 s. An inversion of `internal/domain/cost.go` that is red at `test`: 52.3, 59.5 and 51.4 s. |
| Gate budget | Worst case is three green runs, the baseline plus two that escape: about 261 s. The typical case is about 190 s. That leaves about 25 min of the 30 min `wall_clock` for the QA agent. |
| QA agent run time | Not measured. |

## Consequences

- An inversion that does not compile bites through `build`. The prompt asks
  for candidates that still compile, but the fold cannot tell a regression the
  tests caught from a broken build.
- When the base advanced after the pull request was opened, `git diff base
  head` also shows the base's own changes. The QA agent reads that noise.
  Admission only counts the pull request's production paths, so noise on
  other paths never admits a candidate.
- On the pilot as it stands, the baseline is red at `setup[1]` (Measurements),
  so a production diff fails with `gate_failed` before any agent runs. The
  pilot contract has to get its toolchain right before a verdict is possible.
- Every production pull request now costs one QA agent run and up to three
  gate runs on top of implement's. The cost is on the verify run.
- A verify run needs the QA agent wired. Without `FORJA_MODEL_API_KEY`,
  `forja.verify` refuses with the same message as `forja.implement`, even for
  a markdown-only pull request.
- A QA agent that rewrites history below the head makes `Adopt` refuse the
  candidate. That is a tool error, not a rejection.
- `forja/qa` failure blocks nothing until it is required in the ruleset (#4).

## What only the human can do

- [ ] Ratify the four admission rules, the 4 candidates and 2 inversions, the
      reading of "reverts the whole diff" and the fixed descriptions. On
      ratification, change this ADR's status to `accepted`.
- [ ] Fix the pilot contract's `setup` so the baseline can be green on a
      `golang:1.25` workspace (Measurements).
- [ ] Run `forja.verify` with `FORJA_GITHUB_TOKEN`, a model key
      (`FORJA_MODEL_API_KEY`, `FORJA_MODEL_TOKEN_CAP`, `FORJA_AGENT_BINARY`) and
      Docker on a covered pilot pull request and on an uncovered one. Check
      `forja/qa` success with both red gates in the evidence on the first, and
      `forja/qa` failure naming the escaped inversion on the second. Record the
      QA agent's time and tokens.
- [ ] Require `forja/qa` on `main` in the ruleset (#4).

## References

- `docs/PRD.md`, "forja.verify" and the risk table.
- [ADR 0048](0048-verify-not-verifiable.md), [ADR 0042](0042-implement-delivery.md),
  [ADR 0045](0045-run-cost.md), [ADR 0038](0038-model-proxy.md).
- `CLAUDE.md`, rules 2, 3, 4, 5 and 6.
