# ADR 0027 — Pilot repository and phase-0 maintainer (provisional default)

- **Status:** proposed — default taken by the team; **human ratification pending**
- **Date:** 2026-11-01
- **Issue:** Refs #1 (stays open until ratification)
- **Measured base:** `origin/main` at `f3d380573fee9340e9d798f3977513b768fc478c`

## Context

The PRD makes the approval of phase 0 conditional on choosing a pilot
repository **from another area** and its maintainer ("Pilot repository: to be
defined — condition for approval"). The PRD's criterion for that repository
is: deliberately mediocre tests (to expose the "green and wrong PR" risk),
gates runnable by command (build, lint, test) and at least one integration
test with a real service (e.g. Postgres) to exercise `services`.

That choice is human: it involves another area's consent and naming a person.
The human asked the team to proceed without interruptions. This ADR,
therefore, **does not decide the pilot**: it records the default that allows
building and exercising phase 0 end to end while the real decision does not
arrive, and makes explicit what only the human can do.

## Decision (provisional)

| Item | Default taken |
|---|---|
| Pilot repository | `fuseone-io/fuseone-forja` — this repository (dogfooding) |
| Maintainer | Paulo Bortolotti, owner of the repository |
| Contract | `.forja.yaml` at the root, in the PRD's v1 shape, adapted to this repository |

Why this default and not another: it is the only repository the team can
measure and run end to end without third-party credentials or another area's
consent. It gives the following slices (contract parser, `forja.doctor`,
gates runner) a real contract to validate, instead of an invented example.

### What this default does **not** meet of the PRD's criterion

| PRD criterion | This repository today | Consequence |
|---|---|---|
| Repository from **another area** | It is Forja's own repository | The default does not serve to approve phase 0; it serves to build it |
| Deliberately mediocre tests | 19 unit tests in 4 packages, all passing (see inventory) | Coverage still small, but it is new code: there is no deliberate "mediocre test" |
| Integration test with a real service | None (see "Support services") | The contract's `services` and `needs:` are not exercised by this pilot today |

The real pilot remains the human's decision. When it is named, this ADR is
superseded by another that records the repository, the maintainer and that
repository's inventory, measured by the same method.

## Inventory of today's gates

Source: `Makefile` on `origin/main`. Five gate targets and one aggregator.

| Gate | Command (Makefile) | Aggregated by |
|---|---|---|
| build | `go build -o bin/forjad ./cmd/forjad` | `make check` |
| vet | `go vet ./...` | `make check` |
| lint | `golangci-lint run ./...` | `make check` |
| test | `go test ./...` | `make check` |
| race | `go test -race ./...` | `make check` |

CI (`.github/workflows/check.yml`) runs `make build`, `make vet`,
`make test` and `make race` with `actions/setup-go@v7` reading `go.mod`, and
runs lint through the `golangci/golangci-lint-action@v9` action with
`version: v2.15.0` — not through the `make lint` target.

### Approximate duration — 3 executions per gate

**Measurement environment:** the maintainer's laptop, `go1.27.2 darwin/arm64`,
warm build and module caches, clean worktree at `f3d3805`. It is not Forja's
environment (ephemeral container, cold cache); the numbers below are the
floor, not the run estimate.

Note: `go.mod` declares `go 1.25.0`, but the local toolchain is `go1.27.2`
(output of `go version`). The contract's image is `golang:1.25`, aligned with
`go.mod`.

| Gate | Measured command | Execution 1 | Execution 2 | Execution 3 | Exit |
|---|---|---|---|---|---|
| build | `/usr/bin/time -p make build` | 0,50 s | 0,36 s | 0,21 s | 0 / 0 / 0 |
| vet | `/usr/bin/time -p make vet` | 0,27 s | 0,19 s | 0,19 s | 0 / 0 / 0 |
| lint (as in the Makefile) | `/usr/bin/time -p make lint` | 0,04 s | — | — | **2** (see below) |
| lint (explicit v2.15.0 binary) | `/usr/bin/time -p <scratch>/golangci-lint run ./...` | 1,07 s | 0,41 s | 0,42 s | 0 / 0 / 0 |
| test (as in the Makefile; cached result) | `make test` under `perl -MTime::HiRes` | 0,34 s | 0,21 s | 0,20 s | 0 / 0 / 0 |
| test without cache | `go test -count=1 ./...` under `perl -MTime::HiRes` | 0,74 s | 0,42 s | 0,43 s | 0 / 0 / 0 |
| race (as in the Makefile) | `make race` under `perl -MTime::HiRes` | 2,33 s | 0,19 s | 0,20 s | 0 / 0 / 0 |
| race without cache | `go test -race -count=1 ./...` under `perl -MTime::HiRes` | 1,85 s | 1,52 s | 1,52 s | 0 / 0 / 0 |
| check | `make check` | not measured as a single target: locally it fails at `lint` with the same error below; the sum of the five gates without cache stays under 5 s | — |

`make lint` failed locally with the literal error:

```
golangci-lint run ./...
make: golangci-lint: No such file or directory
make: *** [lint] Error 1
```

The target depends on `golangci-lint` on the `PATH`. In Forja's workspace that
becomes a `setup` step (see "Contract"). To measure lint, the v2.15.0 binary
was called by explicit path (`golangci-lint has version 2.15.0 built with
go1.27.2`).

Why `make test` and `make race` were measured under `perl -MTime::HiRes` and
not under `/usr/bin/time`: this session's sandbox refused `/usr/bin/time -p make
test` ("runs time with the text test in a plain command"); the Perl stopwatch
measures the same wall-clock time, with hundredths resolution.

### Existing tests

`rtk proxy go test -list '.*' ./...` listed **19 test functions in 4
packages**:

| Package | Tests |
|---|---|
| `cmd/forjad` | 2 |
| `internal/domain` | 2 |
| `internal/mcp` | 7 |
| `internal/runstore` | 8 |

## Support services required

**None today.**

- `/usr/bin/git grep -n -i -e postgres -e integration -- '*.go' Makefile go.mod`
  exited with code 1 (no occurrence).
- `internal/runstore` contains only `memory.go` and `memory_test.go`: the run
  store is in memory.
- There is no `integration-test` target in the Makefile.

A real Postgres container becomes necessary when the run store gains the
integration test foreseen in the `CLAUDE.md` layout. At that moment the
contract receives `services.postgres` and the corresponding gate receives
`needs: [postgres]`; this ADR does not anticipate that.

Local Docker available for the measurement: `docker info` → `29.5.2 Ubuntu 24.04.4
LTS aarch64` (Docker Desktop VM). This describes the laptop, not Forja's
runtime.

## Private registries consumed by setup

**None.**

- `go.mod` directly requires only `github.com/modelcontextprotocol/go-sdk
  v1.8.0`; the indirect ones are public modules.
- `rtk proxy go env GOPRIVATE` → empty.
- `go env GOPROXY` → `https://proxy.golang.org,direct`.

The contract's `setup` resolves modules and installs `golangci-lint` via
`proxy.golang.org`. That is egress to a **public** registry, not a private
one; since the workspace only reaches the control plane's proxies, the
registry-proxy slice needs to decide whether `proxy.golang.org` goes through
it. It is recorded here as a `setup` dependency, not as a decision.

## `.forja.yaml` contract

Written in the PRD's v1 shape, adapted to this repository. Summary of the
choices:

| Field | Value | Why |
|---|---|---|
| `image` | `golang:1.25` | Aligned with `go.mod`'s `go 1.25.0`. `docker manifest inspect golang:1.25` confirmed the image in the public registry (digest `sha256:54b6b88db6fe375c6676625d87d668273f85c6d09153635d0cbba89cba7a207a` for `linux/amd64`) |
| `setup` | `go mod download`; `go install .../golangci-lint@v2.15.0` | Lint requires the binary; the version is the same one CI pins |
| `services` | `{}` | No service today |
| `registries` | `[]` | No private registry |
| `gates` | build, vet, lint, test, race | The five Makefile targets, in `make check` order |
| `verify.exclude` | `**/*.md`, `docs/**` | A documentation-only diff is "not verifiable" |
| `proof_paths` | `.forja.yaml`, `Makefile`, `.golangci.yml`, `**/testdata/**`, `**/*_test.go` | What alters the proof; `.golangci.yml` exists at the root and configures lint |
| `human_merge_paths` | `.github/**` | Same value as the PRD |
| `limits` | `wall_clock: 30m`, `max_diff_lines: 400` | Same values as the PRD; the measured gates stay far below |
| `agent_context` | `CLAUDE.md`, `CONTRIBUTING.md`, `docs/` | What the agent reads before coding |

Measured open point, which `forja.doctor` will expose: the `go.mod` of
`golangci-lint v2.15.0` declares `go 1.26.0` (obtained with `curl -sS
https://proxy.golang.org/github.com/golangci/golangci-lint/v2/@v/v2.15.0.mod`).
Under `golang:1.25`, `go install ...@v2.15.0` depends on `GOTOOLCHAIN=auto`
downloading a 1.26 toolchain — **not measured** in this session. If the doctor
goes red on that step, the ways out are: image `golang:1.26`, a version of
`golangci-lint` compatible with 1.25, or an image that already ships the
binary. The choice is the maintainer's.

Validation of the contract's shape (fields, types, defaults) belongs to issue
#6; this ADR does not anticipate it. If `agent_context` does not accept a
directory, `docs/` becomes the list of relevant files.

## What only the human can do

- [ ] Ratify this default as the **build** pilot, or name right away the
      repository from another area and its maintainer.
- [ ] Name the **real** pilot repository (another area), with deliberately
      mediocre tests and at least one integration test with a real service,
      and its maintainer — the PRD's condition for approving phase 0.
- [ ] Confirm that the named maintainer knows they will write that
      repository's `.forja.yaml` and run `forja.doctor` until it is green on
      `main`.
- [ ] Decide whether this repository continues as a second pilot (dogfooding)
      after the real pilot exists.
- [ ] Decide the way out for the lint `setup` under `golang:1.25` (open point
      above), once `forja.doctor` exists and reports the result.

## Commands that produced the numbers

All run in this session, in the worktree at `f3d3805`, on 2026-11-01.

```
/usr/bin/git rev-parse origin/main
go version
<scratch>/golangci-lint --version
gh repo view fuseone-io/fuseone-forja --json owner,defaultBranchRef,visibility,isPrivate
/usr/bin/git config user.name
rtk proxy go test -list '.*' ./...
/usr/bin/git grep -n -i -e postgres -e integration -- '*.go' Makefile go.mod
rtk proxy go env GOPRIVATE
go env GOPRIVATE GOPROXY GONOSUMDB GOFLAGS
docker info --format '{{.ServerVersion}} {{.OperatingSystem}} {{.Architecture}}'
docker manifest inspect golang:1.25 --verbose
curl -sS https://proxy.golang.org/github.com/golangci/golangci-lint/v2/@v/v2.15.0.mod
/usr/bin/time -p make build        (x3)
/usr/bin/time -p make vet          (x3)
/usr/bin/time -p make lint         (x1, exit 2)
/usr/bin/time -p <scratch>/golangci-lint run ./...   (x3)
perl -MTime::HiRes=time -e '... system("make","test") ...'                         (x3)
perl -MTime::HiRes=time -e '... system("go","test","-count=1","./...") ...'        (x3)
perl -MTime::HiRes=time -e '... system("make","race") ...'                         (x3)
perl -MTime::HiRes=time -e '... system("go","test","-race","-count=1","./...") ...' (x3)
```

`<scratch>` is the session's temporary directory where the `golangci-lint`
v2.15.0 binary was installed; it is not part of the repository.
