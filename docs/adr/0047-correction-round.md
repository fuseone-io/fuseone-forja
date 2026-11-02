# ADR 0047: correction round, forja.implement on Forja's own branch

- **Status:** proposed (default taken by the team; awaits human ratification)
- **Issue:** #20
- **Refines:** [ADR 0042](0042-implement-delivery.md), which left to #20 the
  second call on an issue whose pull request is already open ("a
  non-fast-forward push or a 422 on the pull request fails the run"). It
  supersedes nothing.
- **Date:** 2026-11-02

## Context

PRD, "Users and use cases": the developer role in the fix round answers a
review finding by calling `forja.implement` on the same branch with the
finding as input. Issue #20 asks for three things: a simple finding gives a
new commit on the same branch, a new `forja/gates` on the new head and a
comment with the evidence; a branch Forja did not open is refused; the
reservation of the issue holds for the round too.

Before this decision every `forja.implement` call started `forja/<n>` at the
base tip and opened a new pull request. A second call on an issue that already
had one failed: the push was non-fast-forward, or `POST /pulls` answered 422.
`ImplementInput` took only `repository`, `issue` and `base_branch`.

## Decision

### 1. Input

`ImplementInput` gains `branch` and `finding`, both optional on the wire.
Both empty is a first run, unchanged. Exactly one set is a tool error before
any call. A `branch` other than `forja/<issue>` is refused before any I/O, and
so is a `finding` over 64 KiB.

### 2. "Opened by Forja"

Before the claim, so that a refusal takes no claim and records no run (the
same as a refused claim; `implementer.lease` already reads before the claim),
all four of these must hold:

1. the branch is `forja/<issue>`;
2. exactly one open pull request has head `<owner>:forja/<issue>`;
3. its `head.repo.full_name` is the repository (compared without case, as
   GitHub compares repository names);
4. its author `user.login` equals the login Forja's own credential acts as.

When `base_branch` is given it must equal the pull request's `base.ref`. The
round's base is the pull request's `base.ref`. Every refusal is a tool error
wrapping `mcp.ErrNotForjaBranch`. A lookup that fails rather than refuses (the
API is down, the login cannot be read) is a tool error before the claim too.

`github.PullRequests.Find` lists with `state=open`, `head=<owner>:<branch>` and
`per_page=2`, minted with `pull_requests: read`. The owner prefix is
mandatory: the measurer saw a `head` without it ignored with no error, every
pull request of the repository coming back. As a second guard, a listed pull
request whose `head.ref` is not the branch is an error. None wraps
`github.ErrNoPullRequest`, more than one `github.ErrManyPullRequests`.

### 3. Forja's identity

Each credential names the login it acts as:

- the App: `GET /app` signed with the App JWT returns `slug`; the login is
  `slug + "[bot]"`;
- the development PAT: `GET /user` returns `login`.

`mcp.Identity` is declared by the consumer; `forjad` gets it with a type
assertion on the source `TokenSourceFromEnv` returns and does not start when
that assertion fails. With the PAT, "opened by Forja" means "opened by the
PAT's user", so a pull request that user opened by hand also passes rule 4.

### 4. Starting point

`github.Cloner.CloneBranch(repo, base, branch)` runs the same depth-1 clone of
`base` as `Clone`, then fetches `refs/heads/<branch>` at depth 1 into
`refs/forja/start` with the same extraheader environment (no tags, no
submodules, `core.hooksPath=/dev/null`), and checks the branch out there with
hooks off. A branch equal to `base`, or that is not a branch name, is refused
before a token is minted.

`Checkout.Start()` is the commit the agent's branch starts from: the base SHA
after `Clone`, the branch tip after `CloneBranch`. `SHA`, `ReadFile`,
`ListFiles` and `BlobSHA` still answer for the base commit, so the contract and
the `agent_context` are the base's even when earlier agent commits on the
branch rewrote `.forja.yaml` (rule 2). `Adopt` checks "differs from" and
"descends from" against `Start`, so an amend, a rebase or a reset below the tip
is refused and the push stays a plain fast-forward through the unchanged
`github.Pusher`. `agent.Task.BaseSHA` is `Start()`, so the bundle's one
prerequisite is the tip.

### 5. The agent

`agent.Task` gains `Finding`. The prompt adds "## Review finding to address"
after the issue, with the finding quoted in a fence it cannot close. It says
the branch already holds earlier commits, that new commits go on top, and
that a history that does not descend from the current tip is refused. The
finding is never logged, never stored on `domain.Run`, never put in an error
and never quoted in the comment.

### 6. Delivery

A round's stages after the clone are `agent`, `gates`, `push`, `comment`,
`status`; a first run keeps `pull_request` in place of `comment`.

- The comment is `POST /repos/{o}/{r}/issues/{number}/comments` with
  `{body}`, expecting 201, minted with `pull_requests: write`. The body is
  `evidence.Render`, which now names the branch tip the round started from in
  one header row ("Branch tip before this round") when it is set.
- `forja/gates` success goes on the new head with `target_url` set to the
  found pull request's `html_url` (ADR 0042 §6).
- `domain.Run` gains `StartSHA`, and its `PullRequest` is the found one from
  the start of the run, so `forja.status` and the tool's answer name it on
  every outcome of a round.
- A red gate, no commits or a failed agent behave as in ADR 0042 §7: no push,
  no comment, no status.

The round claims `refs/forja/claims/<n>` exactly as a first run does (rule 6),
and releases it on every outcome.

## Measurements

From the measurer, before any code (org `fuseone-io`, git 2.54.0):

| Question | Result |
|---|---|
| `GET /pulls?state=all&head=fuseone-io:<branch>` for 4 branches | one pull request each, 4 of 4, out of 18 in the repository |
| `head=<branch>` with no owner prefix | 18: the filter ignored, no error |
| `head=fuseone-io:no-such-branch-xyz`, `head=phbpx:<branch>` | `[]` and 0 |
| Listed pull request shape | `number`, `html_url`, `state`, `user {login, type}`, `head {label, ref, repo {full_name}, sha, user}`, `base {ref}` |
| `GET /user` with the gh token vs. the author of PR #42 | the same `{"login":"phbpx","id":3296831,"type":"User"}` |
| Comment permission | REST docs and `server-to-server-permissions.json`: `issues: write` **or** `pull_requests: write` |
| Depth-1 clone of `main`, depth-1 fetch of another branch, one commit on top, `push --dry-run` | fast-forward, exit 0, 3 of 3; a commit on the `main` tip instead is `[rejected] (non-fast-forward)` |

Not measured: `GET /app` with a real App JWT (the App is not provisioned,
#4); the `<slug>[bot]` form is taken from two other Apps (`dependabot[bot]`,
`renovate[bot]`). The comment's 201 and its body limit were not exercised
(no POST was made); the 60 000-byte cap of ADR 0042 §8 is kept. The
open-only listing and a server-side push of a ref from a shallow clone to
GitHub were not measured either; a push from a depth-1 clone to a full local
bare repository is exercised by the tests.

## Consequences

- A round makes two more API reads before the claim: the listing and the
  login.
- A round never updates the pull request body; the first run's evidence stays
  there and each round adds a comment.
- The lease of a round is sized from the default branch's contract, like a
  first run's (`implementer.lease`), even when the pull request targets
  another base.
- A branch pushed by a round whose comment or status then failed is left on
  GitHub, as in ADR 0042.
- With the PAT fallback, a pull request the PAT's user opened by hand from
  `forja/<n>` passes "opened by Forja".

## What only the human can do

- [ ] Ratify the identity rule ("opened by Forja" is the author login of the
      credential Forja acts as, `slug[bot]` for the App, the PAT's user in
      development) and the 64 KiB finding cap. On ratification, change this
      ADR's status to `accepted`.
- [ ] Once the App (#4) or the PAT is in place and the pilot contract is green
      (ADR 0042 records that the pilot's `setup[1]` is red), run a real round
      against the pilot: a first run, a finding, and the new commit, comment
      and `forja/gates` on the same pull request.

## References

- `docs/PRD.md`, "Users and use cases".
- [ADR 0042](0042-implement-delivery.md), [ADR 0033](0033-reservation-by-ref-with-lease.md).
- [`docs/github-app.md`](../github-app.md), the permissions.
- `CLAUDE.md`, rules 2, 3, 4, 5 and 6.
