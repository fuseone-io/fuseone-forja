# ADR 0043: the run's limits, wall_clock and max_diff_lines

- **Status:** proposed (default taken by the team; awaits human ratification)
- **Issue:** #19
- **Refines:** [ADR 0042](0042-implement-delivery.md) §7 (how a deadline ends
  a run) and §9 (the one deadline). It supersedes nothing.
- **Date:** 2026-11-02

## Context

The contract's `limits` were parsed into `domain.Limits{WallClock,
MaxDiffLines}` and only half enforced. `wall_clock` was already the run's
deadline (ADR 0042 §9), but a run it ended said nothing useful: a gate it
killed read `gate_failed`, a push or a pull request it cut came back as a tool
error, and ADR 0042 §7 recorded "a deadline ends `failed` with no reason".
`max_diff_lines` was read nowhere. Only the wall clock had a default
(`gates.DefaultWallClock`, 30m), and neither was documented.

The PRD's contract example, the pilot's `.forja.yaml` and ADR 0027 all use
`wall_clock: 30m` and `max_diff_lines: 400`.

## Decision

### 1. Where the wall clock counts from

Issue #19 says the wall clock counts "from the start of the workspace". It
keeps counting from the claim, as `timeLeft` does since ADR 0042 §9. The claim
comes before the workspace, so this is the stricter of the two, and it is what
guarantees the run ends before the claim's lease (ADR 0033). Moving the start
to the workspace launch would break that guarantee and needs an ADR that
supersedes this one.

### 2. Reason `wall_clock`

The run's deadline is built with `context.WithTimeoutCause` and a cause of its
own. When a stage stops the run while that cause holds, the run ends `failed`
with reason `wall_clock`, and whatever error the stage met on the way (a push
or a pull request cut by the deadline) is not returned: like any deadline, it
is a result of `forja.implement`, not a tool error.

- `wall_clock` replaces `gate_failed` (a gate the deadline killed is not a
  red gate) and an empty reason. It never replaces `token_cap`,
  `no_commits` or `diff_too_large`.
- A caller that goes away (cause `context.Canceled`) stays a tool error.
- Killing the agent and the gates and destroying their workspaces on the
  deadline is `agent.Runner`'s and `gates.Runner`'s, as before. The claim is
  released and the run recorded on the detached context, as before.

ADR 0042 §7's "a deadline ends `failed` with no reason" now reads "ends
`failed` with reason `wall_clock`".

### 3. Reason `diff_too_large` and the `diff` stage

A new stage, `diff`, runs after `gates` and before `push`. The lines are
counted once, in the agent stage, right after `Adopt` imported the head; the
`diff` stage compares them with the limit. More lines than the limit: no push,
no pull request, no status, and the run ends `failed` with reason
`diff_too_large`. Exactly the limit passes. A red gate stops the run before the
`diff` stage, so `gate_failed` wins when both fail. A count that cannot be
taken fails the run as a tool error; an uncounted head is never judged under
the limit.

The stage sits after the gates so that the evidence, rendered when the gates
end, already carries the measured lines and the limit.

### 4. How the lines are counted

`Checkout.DiffLines` runs, on the control plane's own clone:

```
git --attr-source=<base sha> diff --numstat -z --no-renames --no-ext-diff --no-textconv <base sha> <head>
```

and sums the added and the deleted lines.

- `--no-renames`: a rename is a delete and an add, whatever `diff.renames`
  says, so moving a file cannot shrink the count.
- `--attr-source=<base sha>`: what is binary comes from the base commit's
  attributes, never from a `.gitattributes` the agent committed. Without it an
  agent could mark its own files `binary` and count zero lines. It is the
  command-line option rather than the `GIT_ATTR_SOURCE` variable because a git
  that predates it refuses an unknown option, and the count then fails as a
  tool error, while it ignores an unknown variable without a word.
- `--no-ext-diff --no-textconv`: no diff driver named by an attribute runs on
  the control plane. git 2.54 runs neither for `--numstat`; the flags keep it
  so whatever git does next.
- A binary file (`-` in both columns) counts zero lines. The 32 MiB bundle cap
  of ADR 0042 still bounds the change.
- Two-dot equals three-dot here: `Adopt` already requires the head to descend
  from the base.
- The head must be a full commit name, so it cannot be read as an option or a
  range.

The count is a control-plane measurement of git (rule 3), not something the
workspace reports, and not a reading of gate output (rule 5).

### 5. Defaults

`domain.DefaultWallClock = 30m` and `domain.DefaultMaxDiffLines = 400`, the
PRD's values. `Limits.Effective()` fills each omitted limit with its default;
`gates.DefaultWallClock` is the domain's. The parser does not apply them:
`domain.Contract` keeps omitted limits zero, so `LeaseFor` still gives a
contract without a wall clock the flat 60-minute `DefaultLease`.

### 6. On the wire and in the evidence

The run carries `Limits`, the effective limits set when the contract is read,
and `DiffLines`, nil until counted. `forja.implement` and `forja.status` gain
`wall_clock_seconds` and `max_diff_lines`, absent only for a run that failed
before it read its contract, and `diff_lines`, absent until counted.
`forja.status` lists the `diff` stage between `gates` and `push`. The evidence
header gains two rows, "Wall clock limit" and "Diff lines (measured / limit)".

## Measurements

Local git 2.54.0 (Apple Git-157).

| Question | Result |
|---|---|
| `git diff --numstat` with `diff.<driver>.textconv` and `.command` configured and `*.txt diff=<driver>` | the driver does not run; raw counts, with or without `--textconv` |
| `git diff` (patch) under the same configuration | the external driver runs |
| `TestCheckout_diffLines_renameCountsBothSides` with `--no-renames` removed | the count is refused: a rename record no longer has one path |
| `TestCheckout_diffLines_agentGitattributesBinary_linesStillCount` with the attribute source removed | 0 lines instead of 4 |
| `TestCheckout_diffLines_gitWithoutAttrSource_refused`, a stand-in git that drops `GIT_ATTR_SOURCE` and rejects `--attr-source`, against the variable | 0 lines and no error; against the option, the count is refused |

## Consequences

- `--attr-source` needs git 2.42 or later on the control plane. On an older
  git every count fails, and with it every run that reaches it, rather than
  letting the agent's own `.gitattributes` decide what counts as binary.
- A run that hits its wall clock while the pull request or the status call is
  in flight may leave a pushed branch or an open pull request without
  `forja/gates` (as ADR 0042 already notes for a failed call).
- Paths under `verify.exclude` still count toward `max_diff_lines`.
- The lines are counted before the gates run, so a red gate's run shows them
  too.

## What only the human can do

- [ ] Ratify the defaults (30m, 400), the counting rule and the reasons. On
      ratification, change this ADR's status to `accepted`.

## References

- `docs/PRD.pdf`, the contract example and "Limites".
- [ADR 0042](0042-implement-delivery.md), [ADR 0033](0033-reservation-by-ref-with-lease.md),
  [ADR 0027](0027-pilot-repository.md).
- `CLAUDE.md`, rules 2, 3, 5 and 6, and "Every run has a deadline".
