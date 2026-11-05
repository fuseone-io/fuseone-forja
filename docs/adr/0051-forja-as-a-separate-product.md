# ADR 0051: Forja as a separate product, with FuseOne as its reference client

- **Status:** proposed
- **Issue:** none yet (PR #51)
- **Refines:** `docs/PRD.md`, Summary and goal 4. It does not decide whether
  FuseOne adopts Forja as its official Level 2, which the PRD leaves for later.
  It supersedes nothing.
- **Date:** 2026-11-04

## Context

The PRD defines Forja as "the execution runtime of the FuseOne software
factory". It leaves orchestration, approval, cost caps and merge policy to
FuseOne. That split is right, but the framing ties Forja to a single caller,
and the code does not have that tie:

- **Interface:** Forja speaks MCP (`cmd/forjad serve`, over stdio). Its tools
  (`forja.doctor`, `forja.plan`, `forja.implement`, `forja.verify`,
  `forja.status`) carry standard MCP annotations, `ReadOnlyHint` and
  `DestructiveHint` (`internal/mcp/tools.go`). FuseOne's Gate reads them, and
  so can any MCP client.
- **Repository contract:** `.forja.yaml` v1 is area-agnostic (PRD goal 1). It
  says nothing about FuseOne.
- **Identity:** Forja has its own GitHub App per installation
  (`FORJA_GITHUB_APP_ID`, `docs/github-app.md`). The reservation lives in git
  (`refs/forja/claims/<n>`, ADR 0033), not in FuseOne's store.
- **Accounting:** cost per run (ADR 0045) and the per-repository view
  (ADR 0049) are already Forja's own. Limits per run (ADR 0043) and the model
  token cap (`FORJA_MODEL_TOKEN_CAP`) are enforced by Forja, not by its caller.
- **References to FuseOne:** the only ones in non-test Go code are comments
  that call the read/write annotations "FuseOne's Gate".

On the FuseOne side, fuseone-agents#154 and NT-013 describe the Level 2 as "a
coding runtime behind one coarse MCP tool". The factory's Developer role
(Level 1, `factory/agents/developer.agent.md` in fuseone-agents, added on
2026-11-02) cannot build or test. It parks every card that needs to with
`factory:needs-runtime`.

Two pressures pull on Forja's shape:

1. **Absorption:** Forja becomes a component inside fuseone-agents. Its
   releases and trust boundary then merge with the orchestrator's.
2. **Sprawl:** a standalone Forja grows a board, roles, approvals and budgets
   so it can be used without FuseOne. It then duplicates what FuseOne already
   does, which is the redundancy this ADR exists to prevent.

## Decision

### 1. Forja is a product with its own lifecycle

Forja keeps its own repository, releases and versioning. Its public contract
is four things, versioned together under semver:

| Surface | Where it is defined |
|---|---|
| MCP tool names, input and output schemas, annotations | `internal/mcp` |
| `.forja.yaml` contract | `internal/contract`, PRD "Repository contract" |
| Commit statuses `forja/gates` and `forja/qa`, and their descriptions | ADR 0042, ADR 0050 |
| Evidence report structure | `internal/evidence` |

A breaking change to any of these is a major version and needs an ADR. A
client pins a major version.

### 2. FuseOne is the reference client, not the only one

Any MCP client may call Forja. FuseOne is the client Forja is designed and
tested against first. Forja's code and docs refer to MCP concepts, such as
"read-only tool" and "destructive tool", never to a specific client's
classifier. The comments in `internal/mcp/tools.go` are reworded to match.

### 3. The non-goals stand, with or without FuseOne

Forja does not orchestrate roles, move cards, decide priority, approve
writes, aggregate budgets per area or merge. None of this changes when the
caller is not FuseOne. A caller that needs those things gets them from its own
orchestrator.

In particular, Forja does not add a trigger of its own, such as a GitHub label
or a webhook. Deciding *when* to run is scheduling, and scheduling belongs to
the caller. A thin client outside Forja can turn a label into a
`forja.implement` call.

### 4. What Forja enforces on its own

Some guarantees cannot depend on the caller. Forja keeps or adds them
regardless of who calls:

- one run per issue (ADR 0033);
- the run's limits, `wall_clock` and `max_diff_lines` (ADR 0043);
- the model token cap per run;
- the repositories it may touch, which are exactly those the installation's
  GitHub App is installed on;
- isolation: no credential in the workspace, and egress only to the proxies
  (ADRs 0035, 0037, 0038);
- evidence and statuses written only by the control plane.

Approval is not on this list. A write tool called without an approving
orchestrator runs on whatever confirmation the client asks for.

### 5. Transport

stdio stays the supported transport. Serving several clients from a shared
deployment needs a network transport (MCP Streamable HTTP) with caller
authentication. That is out of this ADR and gets its own.

### 6. Wording

The README, `CLAUDE.md` and the PRD Summary describe Forja as "an execution
runtime for software factories, exposed over MCP; FuseOne is the reference
integration". PRD goal 4 reads: "Forja owns execution and its per-run limits;
scheduling, approval, aggregate cost and merge belong to the caller, which for
FuseOne is the factory and its Gate."

## Alternatives considered

| Alternative | Why not |
|---|---|
| Fold Forja into fuseone-agents | Forja runs untrusted code in Docker, behind an egress gateway, and the PRD expects a dedicated pool. Sharing a binary and release train with the orchestrator widens the trust boundary of both, and makes every runtime change a FuseOne release. |
| A standalone product with its own orchestration | Duplicates FuseOne's board, roles, Gate, approvals and per-area budgets, and splits where a team must look for its factory's state. |
| Keep the PRD framing unchanged | Works for phase 0. It leaves the contract surface unversioned and invites FuseOne-specific shortcuts in Forja's code, which would make the separation more expensive later. |

## Consequences

- A change to an MCP schema or to `.forja.yaml` must stay compatible within a
  major version. Golden tests on the tool schemas are needed to catch an
  accidental break. They are a follow-up slice.
- fuseone-agents must pin a Forja major version. Its Developer and QA roles
  declare the tools they reach by name, as they do today for the GitHub MCP
  server.
- Outside FuseOne, nobody enforces copilot-mode approval except the client.
  The `DestructiveHint` on `forja.implement` and `forja.verify` is the signal
  a client has. A client that ignores it gets autonomy without a Gate. Forja's
  own limits (§4) still apply.
- Aggregate cost per area exists only where a client adds it up. A standalone
  operator has the per-repository view (ADR 0049) and nothing above it.
- Forja's `.claude/agents/` (developer, QA, architect, triager) are tooling for
  developing Forja itself, not part of the product. This ADR does not turn
  them into a second orchestrator.
- PRD phase 2 ("cost per area in FuseOne") is unchanged. It is FuseOne's
  work, consuming Forja's per-run report.

## What only the human can do

- [ ] Ratify the separation and the four-surface public contract. On
      ratification, change this ADR's status to `accepted`.
- [ ] Decide who owns Forja's releases and who approves a major version.
- [ ] Agree with the fuseone-agents maintainers that the factory treats Forja
      as an external, pinned MCP server. The adoption as official Level 2
      stays a separate decision (PRD, Open questions).
- [ ] Open the follow-up issues: rewording the README, `CLAUDE.md` and PRD
      (§6); golden tests on tool schemas; an ADR for a network transport with
      caller authentication (§5).

## References

- `docs/PRD.md`: Summary, Goals and non-goals, Integration with FuseOne,
  Alignment with the FuseOne factory (NT-013), Risks.
- [ADR 0033](0033-reservation-by-ref-with-lease.md),
  [ADR 0042](0042-implement-delivery.md), [ADR 0043](0043-run-limits.md),
  [ADR 0045](0045-run-cost.md), [ADR 0049](0049-operator-view.md),
  [ADR 0050](0050-verify-inversions.md).
- fuseone-agents: `docs/NT-013-software-factory-pilot.md`,
  `factory/agents/developer.agent.md`, issue #154.
