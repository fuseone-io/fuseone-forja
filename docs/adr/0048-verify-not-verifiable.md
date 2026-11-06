# ADR 0048: forja.verify, the "not verifiable" verdict and forja/qa pending

- **Status:** proposed (default taken by the team; awaits human ratification)
- **Issue:** #21
- **Refines:** [ADR 0042](0042-implement-delivery.md), whose run lifecycle,
  branch `forja/<issue>` and commit-status path this decision reuses. It
  supersedes nothing.
- **Date:** 2026-11-02

## Context

PRD, "forja.verify": the verdict has three values. **Not verifiable** means
the diff only touches excluded paths or has no production code. It publishes
`forja/qa` neutral, never green, and the merge stays with a person. The two
other values, **bites** and **does not bite**, need the QA agent and the
inversions (#22).

Before this decision `forja.verify` answered "not implemented". Nothing read a
pull request or its files, nothing matched `verify.exclude` or `proof_paths`
(`internal/contract/paths.go` checked only their syntax), and nothing published
`forja/qa`.

This slice covers the first part of the verify flow: read the pull request,
claim the issue, read the contract at the base, classify the diff, publish
`forja/qa` on the head, and record the run and its cost. It launches no
workspace and calls no model.

## Decision

### 1. "Neutral" is a commit status in state `pending`

GitHub commit statuses take `error`, `failure`, `pending` or `success`, and
nothing else. `neutral` exists only as a check-run conclusion. The App
deliberately has no `checks` permission ([`docs/github-app.md`](../github-app.md),
"What the App does not have"), and a PAT cannot create check runs. A `neutral`
check run would also satisfy a required check, which contradicts "the merge
stays with a person".

So `forja.verify` publishes `context: forja/qa`, `state: pending` on the pull
request's head SHA, minted with `statuses: write`. `target_url` is the pull
request's `html_url`. The description is fixed, one per reason:

| Reason | Description |
|---|---|
| `only_excluded_paths` | Not verifiable: only excluded paths changed; a person decides the merge. |
| `no_production_code` | Not verifiable: no production code changed; a person decides the merge. |

Both are under 140 characters. The state is a constant in the status stage.
The description is chosen by the verdict and refused for any verdict other
than `not_verifiable`. This slice never publishes `forja/qa` with state
`success`.

### 2. Which issue is claimed

The issue is the `<n>` of the pull request's head branch `forja/<n>`, the
branch `forja.implement` pushes. The pull request is read before the claim.
This is the same kind of read as implement's pre-claim contract read, which
sizes the lease. The pull request is refused as a tool error, before any claim
and with no run recorded, when:

- the head ref is not `forja/<positive int>` written canonically (`forja/012`,
  `forja/+12` and `forja/12/x` are refused);
- the head repository is not `repository` (a fork);
- `head.repo` is null (the head's repository is gone);
- the base ref is empty.

A refused claim is a tool error that names the holder, as in implement.

### 3. The diff comes from the API

`github.PullRequests.Read` mints `pull_requests: read` for each call and:

- reads `GET /repos/{o}/{r}/pulls/{n}` for `head.ref`, `head.sha`,
  `head.repo.full_name`, `base.ref`, `html_url` and `changed_files`;
- pages `GET /repos/{o}/{r}/pulls/{n}/files?per_page=100&page=k` until a short
  page, stopping at most one page past the 3000-file cap;
- counts a rename with both its `filename` and its `previous_filename`.

The read is an error, never a verdict, when the number of files listed differs
from `changed_files`. That happens when the pull request changes more than the
3000 files the API lists, or when it moved between the two reads. One page of
files carries patches, so it is read up to 64 MiB; every other response keeps
the 1 MiB bound. An unknown number wraps `ErrPullRequestNotFound`. Errors name
the repository and the number, never the title, the body, a path or a patch.

### 4. Production code is a fold over the base contract

The contract is the one read from the clone of the pull request's base branch
at its pinned commit, never the head's. `base_sha` and `contract_sha` are
pinned in the run, as in implement. Each changed path is **excluded** when
`verify.exclude` matches it, otherwise a **proof path** when `proof_paths`
matches it, otherwise **production code**. So a path both lists match counts
as excluded. `verify.Classify` then decides:

| Changed paths | Outcome |
|---|---|
| none | not verifiable, `no_production_code` |
| every one excluded | not verifiable, `only_excluded_paths` |
| every one excluded or a proof path, at least one not excluded | not verifiable, `no_production_code` |
| any production code | production: the run fails with `inversion_unavailable` and publishes no status |

`excluded` is the sorted, deduplicated list of changed paths `verify.exclude`
matched, whatever the outcome.

### 5. Globs match segment by segment

`domain.MatchPath` splits pattern and path on `/`. A `**` segment matches zero
or more segments. Every other segment goes through `path.Match` against one
segment, so `*` never crosses a slash. Paths carry no leading slash.

- `**/*.md` matches `README.md` and `docs/adr/x.md`, and not `x.mdx`.
- `migrations/**` matches `migrations/a/b.sql` and `migrations`.

The match walks the pattern once, keeping which prefixes of the path it can
end at. A pattern with many `**` segments costs its length times the path's.
It lives in `internal/domain` beside `MatchRegistry`, so the `proof_paths`
signal (#18) reuses it.

### The run, forja.verify and forja.status

The run reuses implement's lifecycle, now shared:

- the lease is sized from the contract;
- the claim is taken before the clone;
- the records are written on a context detached from the caller's, each
  bounded by `FinishTimeout`;
- the release happens on every outcome;
- the work runs under the base contract's wall clock counted from the claim.

The stages are `claim`, `clone`, `diff`, `status` and `release`. A production
diff stops after `diff`, which fails, and goes straight to `release`.
Tokens are 0.

`forja.verify` answers:

- always: `run_id`, `state`, `excluded`, `tokens_in` and `tokens_out`;
- once known: `verdict`, `verdict_reason`, `failure_reason`, `base_sha`,
  `contract_sha`, `head_sha`, `pull_request {number, url}` and `evidence`.

The evidence is `evidence.RenderVerify`. It is deterministic and pins the run,
the pull request, the issue, the base branch and the three SHAs, the verdict
and its reason, and every excluded path as a code span. It is returned, not
posted.

`forja.status` gains `verdict` and `verdict_reason`, present once known, and
`excluded`, always on the wire and empty for any run that is not a verify run.

## Measurements

From the measurer, before any code. Every `gh api` call was a read with the
user's OAuth token.

| Question | Result |
|---|---|
| Commit status states | REST description `info.version 1.1.4`: `state` enum `error, failure, pending, success`. "neutral" appears 0 times on the statuses page. A POST with `neutral` was not sent. |
| Check-run conclusions | `action_required, cancelled, failure, neutral, success, skipped, stale, timed_out` |
| What passes a required check | Docs only: `success`, `skipped` and `neutral`. "Required status checks can be checks or commit statuses". That `pending` blocks is inferred, not observed: the pilot ruleset `26307933` is `disabled` with no `required_status_checks`, and 4 public default branches had none. |
| `GET pulls/42` on the pilot | `number`, `html_url`, `state`, `head.ref`, `head.sha`, `head.repo.full_name`, `base.ref`, `changed_files: 52` |
| `head.repo` of a deleted fork | the whole object is `null`, while `head.ref` and `head.sha` stay (kubernetes/kubernetes#135596, golang/go#9299). The OpenAPI schema says non-nullable; the fake follows the live answer. |
| `GET pulls/41/files?per_page=100` | 200, 5 entries for `changed_files` 5, keys `additions, blob_url, changes, contents_url, deletions, filename, patch, raw_url, sha, status` |
| A rename | pilot PR 32: `status: renamed`, `filename` and `previous_filename` |
| `per_page` above 100 | kubernetes#102147 (336 files): `per_page=250` silently returns 100; paginated, 336 equals `changed_files` |
| The 3000-file cap | DefinitelyTyped#68529: `changed_files` 8559, page 30 has 100 entries, page 31 has 0, with no error |
| `pull_requests: read` subset token, and what the PAT needs | not measured: no App or PAT credential was available. The docs say a subset token cannot exceed the App's permissions, and the fine-grained PAT table lists both endpoints under "Pull requests: read". |

## Consequences

- `forja/qa` pending is indistinguishable, by state alone, from a verification
  still in progress. The description is what tells a person it is final.
- The head SHA is read once, before the claim. A push to the pull request
  after that read gets the status on the older head, which GitHub shows as
  outdated. A push between the pull request read and the files read is
  refused by the `changed_files` check only when the count changes.
- A diff with production code fails with `inversion_unavailable` until #22
  lands. FuseOne sees a failed run, not a tool error.
- `forja.verify` never checks the head out. No workspace, no model and no
  agent token is involved.
- `forja.status` has one more key (`excluded`) for every run.

## What only the human can do

- [ ] Ratify `pending` as the neutral state, the fixed descriptions, the
      `forja/<n>` claim rule and the exclusion precedence. On ratification,
      change this ADR's status to `accepted`.
- [ ] Require `forja/qa` on `main` in the ruleset (#4). Until then a pending
      `forja/qa` blocks nothing. Once it is required, a pending one keeps the
      merge with a person, as the PRD asks.
- [ ] Optionally, run `forja.verify` with `FORJA_GITHUB_TOKEN` on a markdown-only
      `forja/<n>` pull request of the pilot and see `forja/qa` pending on its
      head.

## References

- `docs/PRD.md`, "forja.verify".
- [ADR 0042](0042-implement-delivery.md), [ADR 0033](0033-reservation-by-ref-with-lease.md).
- [`docs/github-app.md`](../github-app.md), the permissions the App has and
  has not.
- `CLAUDE.md`, rules 2, 3, 4 and 6.
