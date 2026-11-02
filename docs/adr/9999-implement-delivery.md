# ADR 9999: from the agent's commits to a pull request with forja/gates

- **Status:** proposed (default taken by the team; awaits human ratification)
- **Issue:** #13
- **Refines:** [ADR 0038](0038-model-proxy.md), which left to #13 whether the
  gates reuse the agent's workspace and whether the model proxy is revoked
  before they run. It supersedes nothing.
- **Date:** 2026-11-02

## Context

PRD, "Fluxo de forja.implement", steps 5, 7 and 8: the control plane runs the
base contract's gates on the agent's tree and keeps each gate's raw log and
exit code. When every gate is green it pushes, opens the PR with the report
and publishes `forja/gates` on the head SHA. Then it destroys the workspace,
releases the claim and returns the PR, the evidence and the cost. The PRD's
security section adds "Push só em branch própria" and "Evidência assinada".

Before this decision `forja.implement` stopped at the agent. `agent.Runner`
halted and closed the workspace without reading the agent's commits back, and
a run was succeeded on the agent's exit code alone. Nothing pushed, opened a
pull request or published a status.

## Decision

### 1. The branch

Before the agent starts, the control plane creates the local branch
`forja/<issue>` at the base SHA in its own clone (`Checkout.StartBranch`). That
tree, `.git` included, is what the agent workspace receives; the agent commits
on that branch. The prompt names the branch.

### 2. Export, inside the workspace

When the agent exits 0 on its own, uncapped and before its deadline, and
before the workspace is halted, the workspace runs:

```
git -c safe.directory=/workspace bundle create -q /forja/commits.bundle <base>..refs/heads/forja/<n>
```

A branch still at the base exits 3 instead (git refuses an empty bundle), which
`ExportCommits` reports as `domain.ErrNoCommits`. The control plane copies
`/forja/commits.bundle` out with `CopyFromContainer` and keeps it only as a
single regular tar entry of at most `workspace.MaxBundleBytes` (32 MiB),
written with `O_EXCL` and mode 0600 into a fresh temporary directory, which is
removed when the run ends. A symlink, a directory, a second entry or an
oversized file is refused and leaves nothing behind. The base and the branch
are checked against fixed patterns before they reach the shell.

### 3. Import, on the control plane

The bundle is not trusted. `Checkout.Adopt`:

1. runs `git bundle verify`;
2. fetches only `refs/heads/forja/<n>` from it into `refs/forja/adopted`, with
   `core.hooksPath=/dev/null`, no tags and no submodules;
3. requires the head to differ from the base (otherwise `ErrNoCommits`) and the
   base to be its ancestor;
4. checks the head out with `checkout --force -B forja/<n>`, hooks disabled.

The workspace's `.git` is never copied: its configuration and hooks would then
run on the control plane. After `Adopt`, `Dir()` holds the head's tree while
`SHA`, `ReadFile`, `ListFiles` and `BlobSHA` still answer for the base commit.

### 4. The gates run in a fresh workspace

The gates run through the existing `gates.Runner`, the same path as
`forja.doctor`, on the control-plane checkout at the agent's head, with the
contract parsed from the base checkout before the agent ran. By then the agent
workspace and its model proxy are gone. Untracked leftovers, processes and the
proxy cannot influence the gates. `writeTar` archives a symlink as a link and
never follows it, so an agent symlink cannot pull control-plane files into the
gates workspace.

The run's `egress_denied` is the agent workspace's denials followed by the
gates workspace's; `registry_access` is the gates'.

### 5. Push

With every gate green, the control plane pushes `<head>:refs/heads/forja/<n>`
from its clone (`github.Pusher`):

- never forced, no hook (`core.hooksPath=/dev/null`, `--no-verify`), no
  submodule;
- the token is minted with `contents: write` and reaches git only as the
  extraheader scoped to the remote URL, exactly as the clone's does;
- any destination other than `forja/<positive int>`, or a head that is not a
  full commit name, is refused before a token is minted or git starts. This
  is the code half of "push em main é recusado". The other half is the
  ruleset on `main`, which is not active today (see below).

### 6. Pull request, then status

Then `POST /repos/{o}/{r}/pulls` with head `forja/<n>`, the base branch the
clone resolved, the issue's title and the evidence as body, minted with
`pull_requests: write`. Then `POST /repos/{o}/{r}/statuses/{head}` with
`context: forja/gates`, `state: success`, a fixed one-line description and
`target_url` set to the pull request's `html_url`, minted with
`statuses: write`. This is PRD step 7 in order. A refusal is a `*StatusError`
carrying the status and the API's message, never the body sent nor the token.

### 7. A red gate, no commits, an agent that failed

A red gate: no push, no pull request, no status. The run ends `failed` with
reason `gate_failed`, `failed_step` and the gate report, raw tails included,
on the run. An agent that exited 0 without a commit ends `failed` with reason
`no_commits`, before any gate. A non-zero exit or a deadline ends `failed`
with no reason; the cap keeps `token_cap`. Each of these is a result of
`forja.implement`, not a tool error. A failure of the infrastructure (clone,
agent launch, adoption, gates runner, push, pull request, status, release) is
a tool error, after the run was recorded failed.

### 8. Evidence and log links

`internal/evidence` renders the pull request body, purely and
deterministically: `Refs #<n>`, the run id, the base branch, the base commit,
the blob of `.forja.yaml` at the base, the head, every setup step and gate with
command, status, exit code and duration, tokens in and out, and the elapsed
wall time from the injected clock. Each step's streams are shown by their last
2 000 bytes, cut on a character, in a `<details>` block whose fence the output
cannot close, then "Full tail: `forja.status run_id=<id>`". Forja has no log
host, so "links aos logs" is the run id plus the inline tails. The body is
capped at 60 000 bytes, below the 65 536 characters GitHub is known to accept
in a pull request body; logs that do not fit are left out with an explicit
note and the footer is always kept. The renderer never interprets the output.

### 9. One deadline

One run deadline covers the agent, the export, the adoption, the gates, the
push, the pull request and the status: the contract's wall clock counted from
the claim, the end `timeLeft` already computed for the agent alone.

### forja.implement and forja.status

`forja.implement` answers `state`, `failure_reason`, `failed_step`,
`base_sha`, `contract_sha`, `head_sha`, `branch`, `pull_request {number,
url}`, `evidence`, `tokens_in`, `tokens_out`, `setup[]` and `gates[]`.

`forja.status` gains `stages[]` (`claim`, `clone`, `agent`, `gates`, `push`,
`pull_request`, `status`, `release`, each `{name, started_at, finished_at?,
outcome}`), `setup[]` and `gates[]`, always on the wire, and `base_sha`,
`contract_sha`, `head_sha` and `pull_request`, present once known. Each stage
is recorded when it starts and when it ends, so a run in flight shows its
current stage with no `finished_at`.

## Measurements

Local git 2.54.0 (Apple Git-157), local Docker on colima 29.5.2, linux/arm64.

| Question | Result |
|---|---|
| `git bundle create <base>..refs/heads/forja/<n>` in a byte copy of a depth-1 clone | one prerequisite, the base (`-<base>` header line) |
| `git bundle verify` and `fetch` of that bundle in the control plane's depth-1 clone | okay; the branch fetched, the base its ancestor |
| Bundle of a branch still at the base | `fatal: Refusing to create empty bundle.`, exit 128 |
| Bundle of an orphan branch | verifies as "complete history"; `merge-base --is-ancestor <base> <head>` exits 1 |
| `TestDocker_exportCommits_bundleOfAgentCommitOnly`, real Engine | a commit made in a `LaunchAgent` workspace adopted on the control plane; an uncommitted file not; a branch at the base `ErrNoCommits`; a 33 MiB random commit refused with nothing written; nothing labelled with the run after `Close` |

From the measurer, before any code:

- The PR response shape (`number`, `html_url`, `head`, `base`) is the one
  `gh api repos/fuseone-io/fuseone-forja/pulls/38` returns; the 422 shape is
  the REST description's `validation-error`. The duplicate-head message text
  was not measured; nothing decides on it.
- The status response is the REST description's example. Its 140-character
  description limit is documented only for deployment statuses; the
  description Forja sends is 63 characters.
- No pull request body limit appears in the REST description; 60 000 bytes is
  a margin under the commonly reported 65 536 characters, not a measurement.
- A `git push --dry-run` with the clone's extraheader against GitHub, with the
  classic PAT, exits 0 (`[new branch]`). A push from a depth-1 clone to a full
  local bare repository exits 0.
- The ruleset on `main` (`26307933`) is `disabled`, with a user in
  `bypass_actors`; `main` is not protected.
- On `main`, the pilot contract stops at `setup[1]`: `golangci-lint` v2.15.0
  needs go ≥ 1.26 and `golang:1.25` has 1.25.14 with `GOTOOLCHAIN=local`. No
  gate runs and `forja/gates` cannot turn green on the pilot until
  `.forja.yaml` changes (ADR 0035 already measured the fix).

## Consequences

- An implement run launches two workspaces in turn, the agent's and the
  gates', each with its own gateway. The gates pay their own setup.
- The bundle's 32 MiB bounds what is copied; it does not bound what the pack
  inflates to when it is checked out on the control plane.
- A non-fast-forward push or a 422 on the pull request fails the run (#20).
  A branch pushed before the pull request or the status failed is left on
  GitHub.
- With the `FORJA_GITHUB_TOKEN` fallback the pull request is authored by the
  token's user, not by the App.
- `forja.doctor` does not record stages and its `forja.status` shows `stages`,
  `setup` and `gates` empty.

## What only the human can do

- [ ] Ratify the export by bundle, the fresh gates workspace, the 32 MiB cap
      and the 60 000-byte body cap. On ratification, change this ADR's status
      to `accepted`.
- [ ] Apply the ruleset on `main` with no bypass for the App (#4): until then
      a push to `main` is refused only by Forja's code.
- [ ] Install the App (#4), or accept the `FORJA_GITHUB_TOKEN` fallback for the
      first real run.
- [ ] Fix the pilot contract's `setup[1]` so `forja.doctor` is green on `main`,
      then run `forja.implement` on a trivial issue of the pilot (ADR 0027) to
      verify "aberta pelo App" and `forja/gates` on the head.

## References

- `docs/PRD.pdf`, "Fluxo de forja.implement" and "Segurança e isolamento".
- [ADR 0038](0038-model-proxy.md), [ADR 0035](0035-egress-gateway.md),
  [ADR 0033](0033-reservation-by-ref-with-lease.md).
- [`docs/github-app.md`](../github-app.md), the permissions and the ruleset.
- `CLAUDE.md`, rules 2, 3, 4, 5 and 6.
