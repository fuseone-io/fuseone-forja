# Contributing to Forja

Thanks for helping. This page is the short version of how we work; the engineering rules in [CLAUDE.md](CLAUDE.md) apply to humans and agents alike, and the operating rules for agents that write to this repository are in [.claude/agent-rules.md](.claude/agent-rules.md).

## Pick something to work on

- The backlog is [issues #1–#24](https://github.com/fuseone-io/fuseone-forja/issues): vertical slices derived from the [PRD](docs/PRD.pdf), each with a "Blocked by" list. Start with one whose blockers are closed.
- `ready-for-agent` means the issue is fully specified and an AFK agent can take it. `ready-for-human` means it needs a decision, external access or a measurement first. Milestones `Fase 0` and `Fase 1` give the order.
- Comment on the issue before you start, so two people or two agents don't take the same one.
- Ask in the issue first when the change touches one of the non-negotiables in `CLAUDE.md`, adds a dependency, changes the `.forja.yaml` contract or any MCP tool schema, or contradicts an accepted ADR in `docs/adr/`. The last two need an ADR, not a workaround.

## Set up

- Go 1.25 or later, golangci-lint v2.15.0 (`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0`), Docker (the workspace runtime of the first slices), `gh` authenticated against `fuseone-io`.
- `make check` runs build, vet, lint, tests and race; `make serve` starts the MCP server over stdio. The README says how to register it in a client.
- Running a slice against the pilot repository needs the Forja GitHub App installed there ([#4](https://github.com/fuseone-io/fuseone-forja/issues/4)). Nothing in this repository needs your personal GitHub token.
- Working with Claude Code: the roles in [`.claude/agents/`](.claude/agents/) (architect, developer, qa, security-reviewer, triager, measurer) and the rules they read in [`.claude/agent-rules.md`](.claude/agent-rules.md).

## How we work

- **Tests first.** A behaviour change starts with a test that fails; a bug fix starts with a regression test that fails on `main` and stays in the tree. Behaviour-preserving refactors are exempt.
- **The non-negotiables are tests.** A slice that touches the contract has a test proving it is read from the base commit's SHA; one that touches the workspace has a test proving no credential is in its environment; one that touches a status has a test proving the workspace cannot write it; one that touches a gate has a test proving only the exit code decides.
- **Never mock what you do not own.** A real Postgres container for the run store, a real Docker container for the workspace, and a fake for GitHub that enforces the API's invariants (a ref that exists cannot be created). No mock of a driver or an SDK.
- **No secrets.** No `.env`, no tokens, no App private key in the tree. Environment variables and the vault.
- **Docs are Markdown.** The PRD arrived as a PDF; every new document in `docs/` is Markdown, and ADRs live in `docs/adr/`.
- **Language.** Everything in the repository is English, documents in `docs/` included. Only GitHub issues may be pt-BR; one document, one language.

## Commits and pull requests

- [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/), scoped and imperative: `feat(claim): …`, `fix(gates): …`. Types: `feat`, `fix`, `refactor`, `test`, `docs`, `chore`, `perf`. One logical change per commit; more than five files is probably two commits. The body says why, not what the diff shows.
- Work on a branch; `main` is never rewritten and never pushed to directly. Amending and force-pushing your own PR branch is fine for humans; agents never force-push.
- Before opening the PR, run the checks for the areas you touched, one variable per command, and put the real exit codes in the PR:

  | Area | Checks |
  |---|---|
  | Go | `make build`, `make vet`, `make lint`, `make test` |
  | Store, run state or SQL | `make integration-test` (real Postgres) |
  | `internal/` | `semgrep --config p/security-audit --config p/secrets internal/` |
  | `deploy/` | `helm lint deploy/helm/forja` |
  | Everything | `make check` — build, vet, lint, test, race — on the final commit |

- The PR body carries the gate table with the exit codes and the sha each ran on, a `Documentation:` line (`none`, `contract`, `runbook`, `new ADR`, `supersedes ADR NNNN`) and `Closes #<n>`. Put `Closes` in the description, not in commits.
- A PR that touches tokens, the proxies, egress, the claim, the contract reader or the evidence gets a security review before merge; say so in the description.
- A new ADR is born as `docs/adr/9999-<slug>.md` and is renamed to the PR number before the PR reaches review.
- PRs merge by rebase, with CI green. The merge is done by a human.

## Report a problem

Open an [issue](https://github.com/fuseone-io/fuseone-forja/issues/new) with what you ran, what you expected and the exit code or output you got. For anything that could let a workspace reach a credential, write a status or leave the egress policy, do not open a public issue: contact the maintainer directly.
