# Forja's GitHub App — provisioning runbook

- **Type:** runbook (not an ADR: the permissions come from the PRD; what is missing is execution)
- **Status:** default taken by the team; **provisioning and ratification pending from the human**
- **Issue:** Refs #4 (stays open until the App exists and is installed)
- **Date:** 2026-11-01
- **Measured base:** `origin/main` at `ce95147dea46427850e944c22186195fb9e23c89`
- **Assumed pilot repository:** `fuseone-io/fuseone-forja` (provisional default of
  [ADR 0027](adr/0027-pilot-repository.md); if the real pilot is another one, this
  runbook applies to it unchanged, only the installation and ruleset target changes)

## Why this document and not the App

Registering a GitHub App requires GitHub's web interface, as **owner of the
organization** (or member, if the organization allows members to create Apps
— a setting that could not be read in this session; see "Measurements"), and
the private key generated at registration needs to go to the vault the
control plane reads. Neither of the two can an agent do: it has no web
session, has no vault, and must not have the key.

The human asked the team to proceed without interruptions. This runbook,
therefore, leaves ready **everything that is text**: the exact permissions,
the manifest to paste into the "Register a GitHub App from a manifest" flow,
the installation scope, how the control plane reads the credentials, the
ruleset the pilot's `main` needs and the commands that prove the App cannot
push to `main`. What only the human can do is in the checklist at the end.

## App permissions (minimal)

One Forja installation, one App. The permissions below are those of issue #4
and of the PRD; none beyond.

| Permission (API) | Level | For what |
|---|---|---|
| `contents` | read & write | clone the repository, push to the run's own branch, create and remove `refs/forja/claims/<n>` |
| `pull_requests` | write | open and update the run's PR |
| `statuses` | write | publish `forja/gates` and `forja/qa` as commit statuses |
| `metadata` | read | mandatory on every App; lists the repository and its metadata |

What the App does **not** have, on purpose:

- **Branch protection bypass.** The App does not enter the `bypass_actors` of
  any ruleset. `main` stays protected for it exactly as for any collaborator
  without bypass.
- `administration`, `workflows`, `checks`, `issues`, `members`, `secrets`,
  `actions`. None of that is needed for the "maintainer turns the factory on
  in their repo" use case. In particular, without `workflows` the App cannot
  change `.github/workflows/**` — consistent with `human_merge_paths:
  [.github/**]` in the contract.
- Webhooks. Forja is called by FuseOne over MCP, not by GitHub events; the
  manifest is born with `hook_attributes.active: false`.

## Manifest to register the App

Paste the JSON below into the organization's manifest registration flow
(`https://github.com/organizations/fuseone-io/settings/apps/new`, or the
equivalent "Register a GitHub App from a manifest" form). The `url` and
`redirect_url` fields are informational; Forja does not use user OAuth.

```json
{
  "name": "Forja (fuseone-io)",
  "url": "https://github.com/fuseone-io/fuseone-forja",
  "description": "Runtime de execucao da fabrica de software FuseOne. Abre PRs com evidencia verificavel; sem bypass de protecao de branch.",
  "public": false,
  "default_permissions": {
    "contents": "write",
    "pull_requests": "write",
    "statuses": "write",
    "metadata": "read"
  },
  "default_events": [],
  "hook_attributes": {
    "active": false
  },
  "redirect_url": "https://github.com/fuseone-io/fuseone-forja",
  "request_oauth_on_install": false,
  "setup_on_update": false
}
```

Notes on form fields the manifest does not cover:

- **Where can this GitHub App be installed?** → "Only on this account"
  (`public: false` above). The Forja of one FuseOne installation is one App
  per installation, not a marketplace App.
- **Private key:** generate **one** key at registration and take the `.pem`
  straight to the vault. Do not leave the downloaded file on the laptop; do
  not paste it into an environment variable, an issue or a chat.
- Note the **App ID** (visible on the App's page) and, after installing, the
  **Installation ID** (last segment of the URL
  `https://github.com/organizations/fuseone-io/settings/installations/<id>`).

## Installation scope

**Only the pilot repository.** On the installation screen, choose "Only select
repositories" and tick only `fuseone-io/fuseone-forja` (or the real pilot,
once named). Never "All repositories": Forja only needs to reach the
repository whose contract it will execute, and the installation's repository
list is the boundary the installation token respects.

When a second repository comes in, it is added to the **same** installation,
one by one, by the human. The installation token the control plane generates
for a run is further restricted to that run's repository (`repositories:
[<repo>]` in the `access_tokens` POST), so that a run never sees a token valid
for another repository of the installation.

## How the control plane reads the credentials

None of this reaches the workspace (rule 4 of `CLAUDE.md`). The workspace
does checkout, build and gates over an already cloned tree; push, PR and
statuses happen in the control plane with the App's token.

| Variable | Content | Mandatory in production |
|---|---|---|
| `FORJA_GITHUB_APP_ID` | numeric App ID | yes |
| `FORJA_GITHUB_INSTALLATION_ID` | numeric Installation ID of the installation on the pilot | yes |
| `FORJA_GITHUB_PRIVATE_KEY_FILE` | **path** to a PEM file mounted by the vault (Kubernetes secret, Vault volume, etc.), readable only by the `forjad` process | yes |
| `FORJA_GITHUB_TOKEN` | fine-grained PAT **restricted to the pilot**, with the same four permissions above | **never in production**; development and tests only |

Rules:

1. The private key **never** goes in an environment variable, not even in
   base64. Only the path. Variables leak in `ps`, in environment dumps and in
   process logs; a file with mode `0400` mounted by the vault does not.
2. The control plane exchanges the key for an **App JWT** (RS256, validity of
   up to 10 min) and the JWT for an **installation token** (validity of 1 h)
   with `POST /app/installations/{installation_id}/access_tokens`,
   restricting `repositories` to the run's repository and `permissions` to
   the subset the step needs (e.g. only `statuses: write` to publish
   `forja/gates`).
3. The installation token lives in the control plane's process, for the
   duration of a run, and is the credential that `clone`, `push`, `pulls` and
   `statuses` use. It is cited in logs only by reference (run id), never by
   value.
4. `FORJA_GITHUB_TOKEN` exists to run `forjad` on the laptop and in the
   integration tests without an App. It enters through the **same interface**
   the App satisfies, so that no code outside the `github` package knows
   which of the two is underneath. When `FORJA_GITHUB_APP_ID` is set,
   `FORJA_GITHUB_TOKEN` is ignored and a warning is logged; when neither is
   set, `forjad doctor` fails with an explicit error, not with a `401` later.

The interface, declared by the consumer of the token (package
`internal/github`, still to be created by the slice that does the first
clone):

```go
// TokenSource yields a GitHub token scoped to one repository for one run.
// The App implementation mints an installation token; the development
// implementation returns FORJA_GITHUB_TOKEN unchanged.
type TokenSource interface {
    Token(ctx context.Context, repo string) (Token, error)
}

type Token struct {
    Value     string
    ExpiresAt time.Time
}
```

Two implementations: `AppTokenSource` (App ID + Installation ID + PEM read
from the path) and `StaticTokenSource` (the PAT). The contract test runs
against both; the App one uses a fake HTTP server that mimics the shape of
the `access_tokens` response (fields `token`, `expires_at`, `permissions`,
`repositories`).

## Ruleset the pilot's `main` needs

Today the pilot's `main` is **not protected** (measurements below). The
ruleset that issue #4's acceptance criterion requires — "direct push to
`main` by the App stays blocked" — is this one, applied to the default
branch:

| Rule | Value | Why |
|---|---|---|
| `deletion` | — | nobody deletes `main` |
| `non_fast_forward` | — | nobody rewrites `main` |
| `pull_request` | `required_approving_review_count: 0`, `dismiss_stale_reviews_on_push: true`, `require_last_push_approval: false` | every change enters through a PR; no direct push, not even by the App. Zero mandatory approvals because the merge is already serial and human (or the workflow's merger with the human's token); the human decides whether to raise it to 1 on ratification |
| `required_status_checks` | context `check` (integration `15368`, GitHub Actions), `strict_required_status_checks_policy: true` | `check` is the only check that exists on `main` today (measured); `forja/gates` and `forja/qa` enter here when Forja starts publishing them |
| `bypass_actors` | **empty** | the App has no bypass; the human account does not need one either, because merge by PR with a green `check` is the only path |

A ruleset named `default` (id `26307933`) already exists in the repository,
with `enforcement: disabled`, only `deletion` and `non_fast_forward`, and a
`bypass_actors` for user `phbpx` in `always` mode. The shortest path is to
**update it** (PUT) instead of creating a second one. Body for
`gh api -X PUT repos/fuseone-io/fuseone-forja/rulesets/26307933 --input ruleset.json`,
run by the human (it is a write):

```json
{
  "name": "default",
  "target": "branch",
  "enforcement": "active",
  "bypass_actors": [],
  "conditions": {
    "ref_name": { "include": ["~DEFAULT_BRANCH"], "exclude": [] }
  },
  "rules": [
    { "type": "deletion" },
    { "type": "non_fast_forward" },
    {
      "type": "pull_request",
      "parameters": {
        "required_approving_review_count": 0,
        "dismiss_stale_reviews_on_push": true,
        "require_code_owner_review": false,
        "require_last_push_approval": false,
        "required_review_thread_resolution": false
      }
    },
    {
      "type": "required_status_checks",
      "parameters": {
        "strict_required_status_checks_policy": true,
        "required_status_checks": [
          { "context": "check", "integration_id": 15368 }
        ]
      }
    }
  ]
}
```

If the human prefers to keep their own bypass for emergencies, the least
permissive form is `bypass_mode: "pull_request"` (bypass only via PR, never
direct push) — and the App stays out of the list in any case.

## Verification after installing

Everything below is a read or a write that **must fail**. `$TOKEN` is an
installation token of the App, generated by the control plane (or, while
`forjad` does not do it, with any tool that signs the RS256 JWT and calls
`access_tokens`). None of these commands uses the private key directly.

1. The ruleset is active on `main` and has the four rules:

   ```sh
   gh api repos/fuseone-io/fuseone-forja/rules/branches/main \
     --jq '[.[] | .type] | sort'
   # expected: ["deletion","non_fast_forward","pull_request","required_status_checks"]
   ```

2. The App is installed **only** on the pilot (requires organization owner):

   ```sh
   gh api orgs/fuseone-io/installations \
     --jq '.installations[] | select(.app_slug=="forja-fuseone-io") | {id, repository_selection, permissions}'
   # expected: repository_selection == "selected"; permissions == {contents:write, pull_requests:write, statuses:write, metadata:read}

   curl -sS -H "Authorization: Bearer $TOKEN" \
     https://api.github.com/installation/repositories \
     | jq '{total_count, repos: [.repositories[].full_name]}'
   # expected: {"total_count":1,"repos":["fuseone-io/fuseone-forja"]}
   ```

3. The App's token does **not** push to `main` (the write has to be refused):

   ```sh
   /usr/bin/git -C /path/to/disposable-clone \
     -c "http.https://github.com/.extraheader=AUTHORIZATION: bearer $TOKEN" \
     push https://github.com/fuseone-io/fuseone-forja.git HEAD:refs/heads/main
   # expected: exit 1, with "GH013: Repository rule violations found for refs/heads/main"
   # and "Changes must be made through a pull request."
   ```

4. The same token **can** push to a run branch and create the claim ref
   (proof that `contents: write` is there):

   ```sh
   /usr/bin/git -C /path/to/disposable-clone \
     -c "http.https://github.com/.extraheader=AUTHORIZATION: bearer $TOKEN" \
     push https://github.com/fuseone-io/fuseone-forja.git HEAD:refs/heads/forja/smoke-4
   # expected: exit 0

   /usr/bin/git -C /path/to/disposable-clone \
     -c "http.https://github.com/.extraheader=AUTHORIZATION: bearer $TOKEN" \
     push https://github.com/fuseone-io/fuseone-forja.git :refs/heads/forja/smoke-4
   # expected: exit 0 (cleans up the test branch)
   ```

5. The App's token has **no** bypass (must be `"never"`; `"always"` or
   `"pull_requests_only"` means the App entered `bypass_actors`):

   ```sh
   curl -sS -H "Authorization: Bearer $TOKEN" \
     https://api.github.com/repos/fuseone-io/fuseone-forja/rulesets/26307933 \
     | jq '.current_user_can_bypass'
   # expected: "never"
   ```

The results of these five commands, pasted into a comment on issue #4, are
the evidence that closes the "no bypass" and "direct push blocked" criteria.

## Measurements made in this session (read-only)

Environment: `gh` authenticated as `phbpx` (OAuth token with scopes `admin:org,
admin:public_key, delete_repo, gist, project, repo`), organization
`fuseone-io`, repository `fuseone-io/fuseone-forja`, `origin/main` at
`ce95147`. All commands below were run on 2026-11-01.

| Claim | Result | Command |
|---|---|---|
| The pilot is public, in the `fuseone-io` organization, default branch `main` | **confirmed** | `gh api repos/fuseone-io/fuseone-forja --jq '{full_name,private,visibility,default_branch,owner_type:.owner.type}'` → `{"default_branch":"main","full_name":"fuseone-io/fuseone-forja","owner_type":"Organization","private":false,"visibility":"public"}` |
| The organization is on the `team` plan | **confirmed** | `gh api orgs/fuseone-io --jq '{login,plan:.plan.name,type}'` → `{"login":"fuseone-io","plan":"team","type":"Organization"}` |
| The pilot's `main` has classic branch protection | **refuted** — it does not | `gh api repos/fuseone-io/fuseone-forja/branches/main/protection` → HTTP 404 `{"message":"Branch not protected"}`; `gh api repos/fuseone-io/fuseone-forja/branches/main --jq '{protected,protection}'` → `{"protected":false,"protection":{"enabled":false,"required_status_checks":{"checks":[],"contexts":[],"enforcement_level":"off"}}}` |
| Some rule applies to `main` today | **refuted** — none | `gh api repos/fuseone-io/fuseone-forja/rules/branches/main` → `[]` |
| Rulesets in the repository | **1 of 1**, disabled | `gh api repos/fuseone-io/fuseone-forja/rulesets` → 1 item: `{"id":26307933,"name":"default","target":"branch","enforcement":"disabled"}`; `gh api repos/fuseone-io/fuseone-forja/rulesets/26307933` → `rules: [deletion, non_fast_forward]`, `conditions.ref_name.include: ["~DEFAULT_BRANCH"]`, `bypass_actors: [{actor_id: 3296831, actor_type: User, bypass_mode: always}]`, `current_user_can_bypass: always` |
| The bypass actor `3296831` is `phbpx` | **confirmed** | `gh api user/3296831 --jq '{login,type}'` → `{"login":"phbpx","type":"User"}` |
| Organization-level rulesets | **not measured** | `gh api orgs/fuseone-io/rulesets` → HTTP 404 `{"message":"Not Found"}` (the token is a member's, not an owner's) |
| Apps installed in the organization | **not measured** | `gh api orgs/fuseone-io/installations` → HTTP 404 `{"message":"Not Found"}`; `gh api user/installations` → HTTP 403 `{"message":"You must authenticate with an access token authorized to a GitHub App in order to list installations"}` |
| Who owns the organization | **confirmed**: `klinux`, `sergiomonteiro`; `phbpx` is **member** | `gh api 'orgs/fuseone-io/members?role=admin' --jq '[.[] | .login]'` → `["klinux","sergiomonteiro"]`; `gh api orgs/fuseone-io/memberships/phbpx --jq '{role,state}'` → `{"role":"member","state":"active"}` |
| Repository admins | **confirmed**: 3 of 3 collaborators are `admin` | `gh api repos/fuseone-io/fuseone-forja/collaborators --jq '[.[] | {login, role_name}]'` → `klinux`, `sergiomonteiro`, `phbpx`, all `admin` |
| A check named `check` exists on `main`, for the ruleset to require | **confirmed** | `gh api repos/fuseone-io/fuseone-forja/commits/main/check-runs --jq '[.check_runs[] \| {name, app_id: .app.id, app_slug: .app.slug}]'` → `[{"app_id":15368,"app_slug":"github-actions","name":"check"}]` (1 check run of 1) |
| Commit statuses on `main` (where `forja/gates` and `forja/qa` will appear) | **0 of 0** today | `gh api repos/fuseone-io/fuseone-forja/commits/main/status --jq '{state,total_count}'` → `{"state":"pending","total_count":0}` |
| Members can register GitHub Apps in the organization | **not measured** | there is no REST read endpoint for that setting; only the owner UI (`Settings → Member privileges → GitHub Apps`) |

### Findings for the human

1. **The pilot's `main` is open today.** No active rule, no classic
   protection; the only ruleset is disabled and, even active, would require
   neither PR nor check. The criterion "direct push to `main` by the App
   stays blocked by the ruleset" is false until the ruleset above is applied
   — for the App and for any collaborator.
2. **Whoever is operating (`phbpx`) is a member, not an owner, of the
   organization.** Registering an App at the organization level is an owner
   action, unless the organization allows members to (not measured). Either
   an owner (`klinux` or `sergiomonteiro`) registers the App, or grants owner
   to whoever will operate Forja, or enables the permission for members.
3. The `check` check exists and is published by integration `15368` (GitHub
   Actions); it is the context the ruleset must require today.

## What only the human can do

- [ ] An **owner** of the organization (`klinux` or `sergiomonteiro`)
      registers the App from the manifest above in `fuseone-io`, or grants
      owner to `phbpx`, or enables "members can create GitHub Apps" — and
      says which of the three they chose.
- [ ] Generate **one** private key at registration and store it in the vault
      the control plane reads; the PEM is mounted as a file at the path in
      `FORJA_GITHUB_PRIVATE_KEY_FILE`. No copy outside the vault.
- [ ] Install the App **only** on `fuseone-io/fuseone-forja` ("Only select
      repositories"). Note App ID and Installation ID in the vault, next to
      the key.
- [ ] Apply the ruleset above to the pilot's `main` (PUT on ruleset
      `26307933`), with `enforcement: active`, `bypass_actors: []` and the
      `check` check required. Decide whether `required_approving_review_count`
      stays at 0 or goes up to 1.
- [ ] Run the five commands of "Verification after installing" and paste the
      outputs (with the exit codes) into a comment on issue #4. Only then are
      the "no bypass" and "direct push blocked" criteria **verified**.
- [ ] Ratify the credential model (`FORJA_GITHUB_APP_ID`,
      `FORJA_GITHUB_INSTALLATION_ID`, `FORJA_GITHUB_PRIVATE_KEY_FILE`;
      `FORJA_GITHUB_TOKEN` only in development) before the slice that creates
      `internal/github`.
- [ ] If the real pilot (another area, ADR 0027) is named before all this,
      point installation and ruleset at it instead of `fuseone-forja`.

## References

- `docs/PRD.pdf` — use case "Maintainer turns the factory on in their repo".
- `CLAUDE.md` — rules 3 (evidence written by the control plane), 4 (no
  credential enters the workspace) and 7 (no secret in the repository).
- [ADR 0027](adr/0027-pilot-repository.md) — pilot repository (provisional
  default).
- Issue #4; issue #1 (pilot).
