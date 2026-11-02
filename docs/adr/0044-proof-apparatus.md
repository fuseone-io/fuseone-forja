# ADR 0044: the proof apparatus and the human-merge label

- **Status:** proposed (default taken by the team; awaits human ratification)
- **Issue:** #18
- **Refines:** [ADR 0042](0042-implement-delivery.md), which delivers a green
  run as a pull request with `forja/gates` and says nothing of what the agent
  changed. It supersedes nothing.
- **Date:** 2026-11-02

## Context

PRD, "Repository contract": "Forja does not promise the agent cannot loosen
the proof; it promises that this is visible. Every change in `proof_paths`,
and every change or removal of an existing test, opens the 'proof apparatus'
section of the evidence and sends the PR to a human merge." The PRD's risk
table repeats it as the mitigation for "the agent loosens a test or a gate to
pass".

Before this decision `proof_paths` and `human_merge_paths` were parsed into
the contract and only their glob syntax was checked. Nothing compared the
agent's head with the base, the evidence had no proof section and no label
was applied: a green run that rewrote the Makefile published `forja/gates`
success on a pull request with no human-merge signal.

## Decision

### 1. What a test is

Forja guesses no language. A test is whatever `proof_paths` declares; the
pilot declares `**/*_test.go` and `**/testdata/**` (ADR 0027). "Forja guesses
nothing."

### 2. Existing

A path is existing when it exists at the base commit, compared file by file.
A `proof_paths` file opens the proof-apparatus section when it is modified,
deleted or type-changed. A file the agent added never opens it: a new test,
even in a new file, does not count. A new test function added to an existing
test file is a modification of that file, so it opens the section.

### 3. Renames

The diff runs with `--no-renames`. Moving an existing test is the deletion of
a file the base had, which opens the section, plus an addition.

### 4. human_merge_paths

Any change under `human_merge_paths` marks the human merge, additions
included: a new `.github/CODEOWNERS` counts. A `proof_paths` hit marks it as
well. A file that matches both lists appears in both.

### 5. Glob semantics

`domain.MatchPath(pattern, path)`:

- a pattern is anchored at the repository root and split on `/`;
- a `**` segment matches zero or more whole segments;
- every other segment is a `path.Match` pattern, so `*` never crosses `/`;
- there is no gitignore-style basename matching: `Makefile` matches only the
  root's `Makefile`, never `sub/Makefile`;
- a malformed pattern matches nothing (the contract parser already refused
  it).

`verify.exclude` (#21) is meant to reuse the same matcher.

### 6. The label

The label is `forja/human-merge`. It is applied with
`POST /repos/{o}/{r}/issues/{n}/labels`, body `{"labels":["forja/human-merge"]}`,
minted with `pull_requests: write`, right after the pull request opens and
before `forja/gates` is published, inside the `pull_request` stage. If GitHub
refuses it the run fails with a tool error at that stage and no status is
published: a green `forja/gates` never sits on an unmarked pull request. This
is fail-closed. The `forja/gates` context, state and description are
unchanged; the status keeps reflecting only the gates.

### 7. When the diff is computed

At adoption, in the agent stage, right after `Checkout.Adopt` returns the
head: `Checkout.Diff` runs

```
git diff -z --no-renames --no-ext-diff --no-textconv --no-color --name-status <base> <head> --
git diff -z --no-renames --no-ext-diff --no-textconv --no-color --numstat     <base> <head> --
```

in the control plane's clone, joins both by path (numstat `-` `-` is a binary
file) and refuses a head that is not a full commit name before git starts. A
status letter other than `A`, `M`, `D` or `T` is an error, never a dropped
change. `domain.ClassifyProof` then sorts the changes by the lists of the
contract parsed from the base commit before the agent ran (rule 2): an agent
that empties `proof_paths` in its own `.forja.yaml` changes nothing.

The diff is a pure function of base..head, known there without a new stage, so
ADR 0042's stage list and the `stages[]` wire stay as they are. A red run's
evidence and `forja.status` show the proof changes too; only the label waits
for the pull request, which exists only after green gates. A diff that fails
is an infrastructure error of the adoption, a tool error per ADR 0042 §7.

### 8. The evidence

Right under `Refs #<n>` the body says **Human merge required.** with the
reason. Then, before `## Steps` so log truncation never drops them:

- `## Proof apparatus`, only when a `proof_paths` file changed;
- `## Human merge paths`, only when a `human_merge_paths` path changed;

each a `File | Change | Lines` table. Each list is bounded at 100 rows and
12 000 bytes, since the paths are the agent's, then "…and N more; full list:
`forja.status run_id=<id>`". Paths go through the same cell and code-span
escaping as commands.

### forja.implement and forja.status

Both gain `human_merge` (bool), `proof_apparatus[]` and `human_merge_paths[]`,
each entry `{path, change, added, deleted, binary}` with `change` one of
`added`, `modified`, `deleted`, `type_changed`. All three are always on the
wire, the lists empty when nothing matched.

## Accepted residual risk

- A newly added test file can neutralise the existing tests of a package
  without opening the section: a `TestMain` that never calls `m.Run`, an
  `init` that exits, a build tag. The PRD exempts new tests explicitly. The
  backstop is the inversions of #22, not this slice.
- `proof_paths` catches only changes to files that already exist at the
  base. A new file that overrides a proof file by lookup order does not count
  there even if listed, so it belongs in `human_merge_paths`, where additions
  count. The pilot's gates are `make <target>`, and GNU make reads
  `GNUmakefile`, then `makefile`, before `Makefile`: an added `GNUmakefile`
  would replace every gate without touching a `proof_paths` file. The pilot
  contract therefore lists `GNUmakefile` and `makefile` under
  `human_merge_paths`. A contract author who adds a proof file read by a tool
  with a lookup order lists its overriding names there too; Forja guesses no
  tool.
- The head's `.gitattributes` is in the working tree the diff runs next to,
  so the agent can make a text file count as binary (no line counts). The
  path and its kind, which decide the classification, are unaffected.
- `.forja.yaml` forces the human merge only when the base contract lists it
  in `proof_paths`; the lists are exactly the base contract's.

## Measurements

From the measurer, before any code; nothing was written to GitHub.

| Question | Result |
|---|---|
| Does `pull_requests: write` alone add a label? | **Not measured** (no credential: `FORJA_GITHUB_TOKEN` unset, no App). Documented: the permissions page lists `POST /repos/{owner}/{repo}/issues/{issue_number}/labels` under both "Issues" and "Pull requests" write, either one sufficient |
| Success response of the add-labels POST | Documented 200, "same response schema as List labels for an issue". The shape the fake returns, an array of nine-field label objects (`id`, `node_id`, `url`, `name`, `color`, `default`, `description`, `archived_at`, `archived_by`), was measured with `gh api -i repos/fuseone-io/fuseone-forja/issues/18/labels` (HTTP 200), a GET. The client reads no field of it |
| Does adding a missing label create it or fail? | **Not measured.** The label is absent from the repository today (`gh api repos/fuseone-io/fuseone-forja/labels/forja%2Fhuman-merge` answered 404). The docs list 200, 301, 404, 410 and 422 and do not say. Creating a label (`POST /repos/{o}/{r}/labels`) is documented under "Pull requests" write too |
| Does a push of a `.github/workflows/**` change fail without `workflows`? | **Not measured** (no push was allowed). Documented: editing `.github/workflows` needs the "Workflows" permission, which the App does not have (`docs/github-app.md`); the gh OAuth token's scopes lack `workflow` too. A workflow change by the agent is expected to fail at the push, which is a tool error of the push stage; the acceptance test for `.github/**` uses `.github/CODEOWNERS` for that reason |

## Consequences

- A pull request that needs a human merge carries the label and the marking;
  FuseOne's merge policy reads the label, not `forja/gates`.
- Until the label exists in the repository, or until it is measured that the
  add-labels call creates it, a run that needs a human merge may fail at the
  label with no status published. That is the fail-closed choice.
- What happens to an existing label on a correction round on the same branch
  is #20's. `max_diff_lines` (#19) is meant to reuse `Checkout.Diff` and
  `domain.FileChange`.

## What only the human can do

- [ ] Ratify decisions 1 to 8 and the residual risks. On ratification, change
      this ADR's status to `accepted`.
- [ ] Create the `forja/human-merge` label once in the pilot repository, or
      measure on a throwaway pull request, with a token that has
      `pull_requests: write` and neither `issues: write` nor `workflows`:
      `curl -sS -i -X POST -H "Authorization: Bearer $T" https://api.github.com/repos/fuseone-io/fuseone-forja/issues/<pr>/labels -d '{"labels":["forja/human-merge"]}'`,
      once while the label is missing and once after it exists.
- [ ] Measure a push of a commit touching `.github/workflows/x.yml` to
      `refs/heads/forja/smoke-18`, with the App token and with the PAT, and
      record the stderr and exit code here.
- [ ] Once #4 (App or PAT) and the pilot contract's `setup[1]` (ADR 0042) are
      fixed, run `forja.implement` on a pilot issue whose change touches the
      Makefile and check the label, the section and a green `forja/gates`.

## References

- `docs/PRD.md`, "Repository contract" and the risk table.
- [ADR 0042](0042-implement-delivery.md), [ADR 0027](0027-pilot-repository.md).
- [`docs/github-app.md`](../github-app.md), the permissions.
- `CLAUDE.md`, rules 2, 3 and 5.
