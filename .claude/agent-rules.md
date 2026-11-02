# Rules for agents that write to Forja

They apply to `developer` and `qa`, and to any agent that commits or runs
gates. Breaking one invalidates the delivery, however good the diff looks.
`CLAUDE.md` at the repository root holds the engineering rules; this file holds
the operating rules. Read both before the first command.

## Git

- NEVER commit, merge or push to `main`. NEVER `git reset --hard`, NEVER `push --force`.
  NEVER `git stash` without `-m`: the stash is shared with other sessions.
- Before the first command: `pwd` and `/usr/bin/git rev-parse --show-toplevel`. Confirm you are
  in YOUR worktree. Every command runs from there; never `cd` into the original checkout.
- In an isolated worktree git is always `/usr/bin/git <subcommand>`, one command per call, with no
  `cd`, `&&`, `;` or shell variable: the rtk hook rewrites `git` and the sandbox refuses the
  rewritten command. Never work around a refusal with plumbing (`commit-tree`, `update-ref`,
  `send-pack`): a commit is `commit`, a push is `push`.
- The sandbox also refuses compound commands WITHOUT git: `cd x; …`, `mkdir -p d && cat > f`,
  `for` loops, `$(…)`. Each Bash call is one simple command run from the worktree. Read files
  with `Read`, write them with `Write`/`Edit`, never with `cat` or a heredoc. The only accepted
  compound form is the gate template below.
- The shell is zsh: an unquoted glob that matches nothing aborts the command. Always quote:
  `--include='*.go'`, `-name '*.yaml'`. To find code use `codegraph explore` or
  `/usr/bin/git grep -n <pattern> -- '*.go'`.
- Explicit base: `/usr/bin/git fetch origin main` and `/usr/bin/git checkout -b <branch> origin/main`.
  Prove it with `/usr/bin/git log --oneline origin/main..HEAD` before opening the PR.
- Push with an explicit refspec: `git push origin <branch>:<branch>`.
- To add a commit to a branch another agent pushed, `cherry-pick`, never `rebase`: rebasing
  rewrites someone else's commit and needs force, which is forbidden.

## Gates — one call, real exit codes

- Run Makefile gates with `scripts/gates.sh [gate...]` in ONE Bash call (default: build vet lint
  test race docker-test). It runs every gate even after a red one, keeps each raw log, prints
  `gate exit time log` per gate plus the sha, and exits non-zero if any gate did. That table is
  what the PR reports. Set `GATES_LOG_DIR` to your scratchpad; `GOLANGCI_LINT=<path>` when
  golangci-lint is not on PATH.
- Outside the script, `make test | tail` returns the exit code of TAIL, not of the test. The
  template is `make test > <log> 2>&1; T=$?` and report `$T`. Never `&&` chaining gates, never
  `2>/dev/null`, never `grep FAIL` as proof: Forja itself only trusts exit codes, and so do you.
- `make test` and `make race` skip the tests that drive the Docker Engine. `make docker-test`
  runs them once, under race, with `FORJA_DOCKER_TEST=1`, and fails if no daemon answers. While
  iterating on `internal/workspace/` or `cmd/forjad/`, run one of them filtered:
  `FORJA_DOCKER_TEST=1 go test ./internal/workspace/ -run '^TestDocker_foo'`.
- Pick the gates by what the diff touches:
  · always: `make build`, `make vet`, `make lint`, `make test`
  · `internal/workspace/` or `cmd/forjad/`: `make docker-test`
  · anything under `internal/` that touches a store, the run state or SQL:
    `make integration-test` (real Postgres container; NEVER hand-build a database URL)
  · iteration and mutation run the FILTERED suite: `go test ./<pkg> -run '^TestFoo'` or
    `make integration-test PKG=./internal/<pkg> RUN='^TestFoo'`. The full suite runs ONCE, at the end.
  · `internal/`: `semgrep --config p/security-audit --config p/secrets internal/`
  · `deploy/`: `helm lint deploy/helm/forja` when the chart exists
  · `scripts/gates.sh` with no argument is the whole battery (= `make check`): build, vet, lint,
    test, race, docker-test. It is the one the PR reports.
- A gate result belongs to the tree it ran on, not to the moment. Run the full battery on a
  commit and keep its sha: `V=$(git rev-parse HEAD)`. Commit the next fix and rerun only if
  `git diff --quiet $V HEAD -- '*.go' '*.sql' go.mod go.sum` exits non-zero. Markdown and ADRs
  do not invalidate it. In the PR table say which sha each gate ran on.

## Order: review converges, then the full battery

- Iterate with what is filtered. The full battery (`scripts/gates.sh`, integration suite, semgrep) is
  expensive: run it ONCE, on the sha the review already approved, never before each review round.
- Sequence: commit → `code-review:pre-review` → fix critical findings and repeat → full battery
  on that sha → `gh pr create`. If the battery fails, the fix goes back to pre-review and the
  battery repeats only what the diff invalidates (sha rule above).
- At most three pre-review rounds. A critical finding on the third is `status: "blocked"` with the
  findings in the report: a round that does not converge is design, not detail.

## Waiting

- Every gate runs in the FOREGROUND with `timeout: 600000` on Bash. An agent that ends its turn
  waiting on `run_in_background` or `Monitor` is NOT woken up: the delivery comes out blocked,
  with no PR.
- Never `sleep` or `until …; do sleep` waiting for a command or a file. Mutations run in sequence,
  each with the filtered suite.

## Temporary files

- Never `rm`: it asks for human approval and a background agent stays stuck until someone sees
  the prompt. A file YOU created (mutation probe, log, script) leaves the worktree with
  `mv <file> <scratchpad>/trash/`; a tracked file comes back with `/usr/bin/git checkout -- <file>`.
- A probe test in a Go package is born with the `zz_` prefix and leaves the worktree before any
  `git add`; `git add` always with explicit paths, never `-A`.
- Disk is shared with other agents. Clones, builds and installed tools go to the scratchpad,
  never into the repository. List in the delivery JSON (`cleanup`) the path of everything you
  created above 100 MB.
- NEVER `go clean -cache`, even with a full disk: the cache belongs to every agent. A full disk is
  `status: "blocked"` with `df -h` in the report.

## Code comments

- English, and only a local invariant that the implementation does not make obvious. Forbidden:
  history ("this used to be"), dates, past measurements, rejected alternatives, implementation
  roadmaps, references to ADRs, plans, issues or PRs.
- A legacy comment next to changed code does not need cleaning — but if you EDIT the comment
  line, the rule applies to it.

## Delivery

- Everything in the repository is English: code, comments, logs, errors, commit messages, PR
  bodies and every document in `docs/` (ADRs, runbooks, contracts). Only GitHub issues and their
  comments may be pt-BR; one document, one language.
- Commit message: `type(scope): description`, imperative, body saying WHY, not what the diff
  already shows. It ends with the `Co-Authored-By` line the session provides.
- The PR body declares a `Documentation:` line (`none`, `contract`, `runbook`, `new ADR` or
  `supersedes ADR NNNN`), carries the gate table with the REAL exit codes and the sha each ran on,
  and `Closes #<n>`.
- The PR body lists every mutation you applied, its reach and the test that killed it. QA attacks
  the complement, not the same set.
- A new ADR is born as `docs/adr/9999-<slug>.md`. Right after `gh pr create`, still in your turn,
  rename it to the PR number (file, title, index line), commit and push. No PR reaches a human
  with `9999`.
- Before `gh pr create`, run the `code-review:pre-review` skill (tool `Skill`) on the FINAL
  commit, in the order above. A critical finding is fixed, or goes to the report with
  `status: "blocked"`; never register an exception on your own.

## Judgement

- Choosing between alternatives the issue ALREADY enumerates is not a blocking doubt — it is
  the job.
- Stop and say so only if the issue is factually wrong about the code. It usually says which
  premise to check before acting; check that premise FIRST.
- Never merge anything. The merge is serial and happens outside the agent, by a human.
