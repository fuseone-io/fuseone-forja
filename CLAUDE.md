# Forja — Core Engineering Guidelines

Execution runtime for the FuseOne software factory: an MCP server that takes
"implement issue X in repository Y" and returns a PR with verifiable evidence.
Orchestration, governance and cost stay in FuseOne; Forja only does what FuseOne
does not — checkout, build, tests and support services in an ephemeral,
isolated workspace.
Functional reference: [docs/PRD.pdf](docs/PRD.pdf). Work is tracked as GitHub
issues #1–#24, each a vertical slice with its blockers listed inline.

**Scope of this file: the Go core** — `cmd/`, `internal/`, `deploy/`.

**Language policy:** code, identifiers, comments and commit messages are in
English, in line with the rest of FuseOne. Documents in `docs/` and issues may
be in pt-BR — the PRD is — but one document, one language.

Documents in `docs/` are Markdown, not HTML or PDF: GitHub renders them, they
diff line by line in review, and nobody has to open a viewer to read one. The
PRD arrived as a PDF; every new document is Markdown.

## Layout

Target layout. The repository is empty today; create a package when the slice
that needs it lands, not before.

```
cmd/forjad/         Single binary: serve | doctor | migrate
internal/
  domain/           Core types: Run, Contract, GateResult, Evidence, Verdict. No I/O, stdlib only
  contract/         .forja.yaml v1 parser and validation, read from the base commit
  claim/            refs/forja/claims/<n> atomic reservation with lease
  github/           GitHub App tokens, clone, push, PR, commit statuses
  workspace/        Ephemeral runtime interface; Docker first, gVisor/Firecracker per ADR
  gates/            setup and gates runner: raw logs and exit codes, nothing else
  evidence/         Report assembled by the control plane; forja/gates and forja/qa
  proxy/model/      Model API proxy: key, per-run token, token cap, metering
  proxy/registry/   Private registry proxy: read-only credentials per area
  agent/            Driver for the agent inside the workspace
  verify/           Inversions and the QA verdict
  mcp/              MCP server: tool registration, schemas, read/write class
  runstore/         Run state, steps, cost; what forja.status reads
deploy/             Helm chart / compose for the control plane and the proxies
docs/               PRD, ADRs, technical notes
```

---

## Non-negotiable rules

### 1. TDD — the test comes first

Every new behaviour has a test written **before** the implementation. The test
fails first, then passes. If you cannot write the test, you do not yet
understand the requirement.

### 2. The contract comes from the base commit

`.forja.yaml` is read from the commit of the base branch, never from the
agent's tree, and that SHA is pinned in the evidence. A change the agent makes
to the contract does not apply to the run that made it.

### 3. Evidence is written by the control plane

The gate report, `forja/gates` and `forja/qa` are produced outside the
workspace. The agent has no path to write them. Code that lets the workspace
influence a status is a bug, never a convenience.

### 4. No credential enters the workspace

Checkout, push and PR use the GitHub App token in the control plane, scoped to
the repository and the run. The model and private registries are reached only
through the proxies. Everything else is denied egress and logged.

### 5. A gate is an exit code

Forja runs the repository's own commands, keeps the raw log and the exit code,
and never interprets test output. If a decision needs to parse stdout, the
decision is wrong.

### 6. One run per issue

A run holds `refs/forja/claims/<n>` with a lease. No work before the claim, no
claim left behind after the run — success, failure or crash.

### 7. No secrets in the repository

No `.env`, no tokens, no credentials. Environment variables and the vault.

---

## Size limits

| Unit | Limit | On breach |
|---|---|---|
| Go file | 300 lines | Split by responsibility, never by "type" |
| Function | 40 lines | Extract a pure function |
| Cyclomatic complexity | 10 | Decision table or polymorphism |
| Function parameters | 4 | Options struct |
| Nesting depth | 3 | Guard clauses and early returns |
| Test file | no limit | Tests are documentation |

At ~250 lines in a Go file, **stop and refactor before continuing**.

---

## Go

### Principles

- **`internal/domain` does no I/O and knows no infrastructure.** If a type there
  needs a driver, an HTTP client or a Docker SDK, it is in the wrong package. A
  pure library is allowed where the stdlib genuinely lacks the answer; reach for
  it rarely, and say in the code why the stdlib was not enough.
- **Dependencies point inward:** `mcp` → `engine of the run` (`gates`,
  `verify`, `claim`) → `workspace` / `github` → `domain`. Never the reverse.
- **Interfaces are declared by the consumer, not the implementer.** The
  `Workspace` interface lives next to the gates runner that uses it, not next to
  the Docker implementation. Go interfaces are structural — the implementation
  never imports the consumer.
- **Accept interfaces, return structs.**
- **`context.Context` is the first parameter** of every function that does I/O.
  Never store a `Context` in a struct.
- **Errors are values.** Wrap with `fmt.Errorf("...: %w", err)`. Sentinels via
  `errors.Is`, typed errors via `errors.As`.
- **Useful zero value.** A freshly declared struct is either usable or fails
  loudly.
- **Every run has a deadline.** `limits.wall_clock` becomes a `context`
  deadline; nothing that touches a workspace runs without one.

### Forbidden

| Forbidden | Use instead |
|---|---|
| Reading `.forja.yaml` from the working tree | The base commit, SHA pinned in the evidence |
| Parsing gate stdout to decide pass/fail | The exit code |
| A token or key in the workspace env or filesystem | The control plane and the proxies |
| Pushing from inside the workspace | The control plane pushes with the App token |
| Reverting a whole diff as an inversion | Inversions touch production code only; new tests stay |
| `panic` outside `init` or a truly impossible invariant | Return an `error` |
| `any` without a stated reason | Generics or a concrete type |
| Mutable package-level state | Injection through a struct |
| `time.Now()` inside business logic | An injectable `Clock` — otherwise tests are not deterministic |
| `math/rand` on an auditable path | Generate the ID at the edge and record it |
| `select` without `case <-ctx.Done()` | Always handle cancellation |
| A goroutine with no owner | Every goroutine has something that stops it |
| Logging agent prompts, issue bodies or logs verbatim | They may carry personal data — log a reference |
| Float for money | Integer micros (`domain.Cost`) |

### Naming

| Item | Convention | Example |
|---|---|---|
| Package | Singular noun, lowercase | `contract`, `claim` |
| Interface | Noun or `-er` | `Workspace`, `Runner` |
| Constructor | `New<Type>` | `NewDocker` |
| Sentinel error | `Err<Condition>` | `ErrClaimHeld` |
| Test | `Test<Unit>_<condition>_<expectation>` | `TestClaim_refAlreadyHeld_secondRunRejected` |

---

## Tests

- **Test observable behaviour.** Never test a getter; never test that the
  language works.
- **One test, one conceptual assertion.** Several `assert`s about the same
  behaviour are fine; about different behaviours, split them.
- **Table-driven** for variations of one behaviour.
- **`t.Parallel()`** in every test that shares no state.
- **Never mock what you do not own.** For GitHub and Docker, a fake behind the
  consumer-declared interface; for Postgres, a real container — never a mock of
  the driver or the SDK.
- **The fake enforces the same invariants as the real thing.** A test that
  passes against the in-memory workspace or claim store must not fail in
  production because the fake was more permissive.
- **The name states the scenario.** `TestGates_firstGateRed_laterGatesSkipped`,
  not `TestGates2`.
- **Property tests** where they fit: contract normalisation, `proof_paths`
  matching and the verdict fold are the natural candidates.
- **Integration tests run the real thing against the pilot contract.** A gate
  that passes only in a fake is not a passing gate.

### Not a test

```go
// FORBIDDEN — asserts that Go works
func TestNewRun(t *testing.T) {
    r := NewRun()
    if r == nil { t.Fatal("nil") }
}
```

### A test

```go
// GOOD — asserts a domain invariant
func TestEvidence_contractChangedByAgent_gatesStillFromBaseSHA(t *testing.T)
```

---

## Commits

- One commit, one logical change. Never squash the whole task into one.
- Format: `type(scope): description` — `feat(claim): reserve issue under refs/forja/claims`
- Types: `feat`, `fix`, `refactor`, `test`, `docs`, `chore`, `perf`
- More than five files touched is probably two commits.

## Definition of done

- [ ] Test written before the implementation, and it failed before it passed
- [ ] `make check` clean — build, vet, lint, test, race
- [ ] No Go file over 300 lines
- [ ] No `any` without a justifying comment
- [ ] `context.Context` threaded through every I/O path
- [ ] Errors wrapped with `%w`, not `%v`
- [ ] No personal data in logs
- [ ] Nothing in the workspace can write a status or read a credential
