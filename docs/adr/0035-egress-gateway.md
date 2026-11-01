# ADR 0035: per-run egress gateway

- **Status:** proposed (default taken by the team; awaits human ratification)
- **Issue:** #10
- **Refines:** [ADR 0026](0026-workspace-isolation.md), section "Egress"
- **Date:** 2026-11-01

## Context

The PRD ("Segurança e isolamento") says the workspace reaches the model
proxy and the package proxy and nothing else, and that everything else is
denied and logged. Its metrics table counts "fuga de isolamento" from those
denied-egress events. ADR 0026 chose one `--internal` Docker network per
run, with the proxies as the only containers on two networks, and said the
control plane logs refused connections per run from the proxies' logs.

Before this decision `workspace.Docker.Launch` created the container with
an empty `HostConfig`: it sat on the default bridge with full internet
egress. No proxy existed. The model proxy (#11) and the registry proxy
(#16) are both blocked by this issue.

There is also a sequencing gap. The pilot contract runs `go mod download`
and `go install golangci-lint@v2.15.0`, and both need the public Go module
proxy. The contract declares `registries: []`, and #16 covers only a
contract's private registries, so no issue owns public module access.
Denying everything would turn the pilot doctor red until some later slice.

## Decision

### One internal network per run

`Launch(ctx, runID, image, tree)` creates the network `forja-run-<runID>`
with `Internal: true` and the label `forja.run=<runID>`. The workspace
container joins only this network. Its `NetworkMode` is the run network
and it never joins the default bridge. Every container and network the
driver creates carries `forja.run=<runID>`, so a leftover can be traced
to its run.

### The gateway is forjad itself

The gateway is a new `forjad gateway -run <id> [-allow host:port,...]`
subcommand, implemented in `internal/proxy/egress`. It is a standard-library
`net/http` forward proxy on port 3128:

- A CONNECT to a `host:port` on the allowlist is tunnelled. The match is
  exact, after lowercasing. The gateway never terminates TLS.
- The gateway refuses with 403 every other CONNECT and every plain HTTP
  request in absolute form, whether or not the host is listed. The
  allowlist admits tunnels only.
- Each refusal writes one JSON line on stdout with the run id, `at`,
  `kind` (`connect` or `http`) and `destination`. The destination is
  `host:port` only and is cut to 255 bytes. Path, query, userinfo, headers
  and body never reach the log, because they may carry personal data.
- The upstream dial refuses any address that is not public after name
  resolution: loopback, RFC 1918 and ULA, link-local, unspecified, and
  100.64.0.0/10. Every run's gateway shares the default bridge. The measurer
  showed that gateway A could open a TCP connection to gateway B's bridge
  address. A listed name pointed at such an address is refused and logged
  like an unlisted one.
- Before serving, the gateway writes one readiness line that names its
  address.
- If a refusal cannot be written to the log, the gateway stops. A
  gateway that keeps serving after its log broke would let later attempts
  go unrecorded. Once it stops, the workspace reaches nothing.
- The clock is injected. `time.Now` is called only in `cmd/forjad`.

### Gateway container

- Image `alpine:3.20`. The control plane copies its own static linux
  forjad binary to `/usr/local/bin/forjad` with `CopyToContainer`, as
  `root:root 0755`, before the container starts. The gateway is therefore
  version-locked to the code that reads its log, and no image registry is
  involved.
- It runs as user `65534` with `CapDrop: ALL` and `no-new-privileges`. Its
  environment is the image's own, with nothing from the control plane.
- On the run network it has the alias `forja-gateway`. It is attached to
  `bridge` only when the allowlist is non-empty. With an empty allowlist
  it has no route out at all.
- The binary path is `workspace.Options.GatewayBinary`, set from
  `FORJA_GATEWAY_BINARY`. When that is unset, Launch uses
  `os.Executable()` on linux and refuses to launch on any other OS. The
  binary must be static (`CGO_ENABLED=0`) and built for the daemon's
  architecture. Otherwise the gateway does not start and the launch fails
  closed.
- Launch returns only after the readiness line arrives, and waits at most
  10 s for it. Any failure removes the workspace, the gateway and the
  network.

### Workspace environment

The workspace's environment is the image's own plus these variables:

```
HTTP_PROXY=http://forja-gateway:3128   HTTPS_PROXY=http://forja-gateway:3128
http_proxy=http://forja-gateway:3128   https_proxy=http://forja-gateway:3128
NO_PROXY=localhost,127.0.0.1,::1       no_proxy=localhost,127.0.0.1,::1
```

A proxy URL is an address, not a credential. The gateway receives no
credential in this slice.

### Configuration

`FORJA_EGRESS_ALLOW` is a comma-separated list of `host:port`. It is
lowercased and matched exactly. It is empty by default, so the gateway
fails closed. A malformed entry is a startup error that names the
variable: a scheme, a path, userinfo, a wildcard, a missing or
non-numeric port, or an empty element.

### Collection and lifecycle

- `Container.Egress(ctx)` reads the gateway's stdout through
  `ContainerLogs`, demultiplexed with `stdcopy`, and parses the JSON lines.
  It refuses the whole log in three cases, because the report is evidence
  and is either whole or refused:
  - a line it does not understand;
  - a line stamped with another run's id;
  - a gateway that is no longer running, which may have missed attempts.
- The gates runner reads the egress after the steps, timed-out steps
  included. It reads on a detached context with its own timeout, before
  `Close`. An egress that cannot be read is a run error.
- `Close` removes the workspace, then the gateway, then the network. It
  joins the errors, treats NotFound as already removed, and is idempotent.

### Domain and status

- `domain.EgressReport{Denied, Count}`. `Count` is exact. The list keeps
  the first 100. A workspace that retries in a loop decides how many
  attempts there are, not how much the control plane keeps.
- `GateReport.Egress` and `Run.Egress` carry the report.
- `forja.doctor` records a run once the contract parses. It runs the gates
  as that run and records the terminal state with the report: succeeded
  only when the report is OK, failed otherwise. It writes that record on a
  detached context, and returns `run_id`. A rejected contract records no
  run.
- `forja.status` exposes `egress_denied_count` and `egress_denied`
  (`[{at, kind, destination}]`). Both are always on the wire, and the list
  is empty when nothing was denied.

## Measurements

Environment: local Docker on colima, server 29.5.2 linux/arm64,
`alpine:3.20`, `golang:1.25` (go1.25.14). The gateway is the forjad binary
cross-compiled from this branch.

### Pilot contract behind the gateway

`FORJA_PILOT_TEST=1 go test ./internal/workspace -run TestPilot`:

| Allowlist | Result | Denials |
|---|---|---|
| `proxy.golang.org:443,sum.golang.org:443,storage.googleapis.com:443` | `setup[0]` passed (5.7 s); `setup[1]` failed, exit 1: `requires go >= 1.26.0 (running go 1.25.14; GOTOOLCHAIN=local)` | 0 |
| empty | `setup[0]` failed, exit 1: `Get "https://proxy.golang.org/...": Forbidden` | 29, all `connect proxy.golang.org:443` |

The `golang:1.25` image sets `GOTOOLCHAIN=local`. As the contract is
written, the toolchain switch that `go install golangci-lint@v2.15.0`
needs never happens. That step fails with or without egress, and its
failure involves no denial. Egress does not cause it.

The same contract was then run with only `setup[1]` prefixed by
`GOTOOLCHAIN=auto`, in memory, without changing `.forja.yaml`:

| Allowlist | Result | Denials |
|---|---|---|
| the three hosts above | every setup step and every gate passed: setup 2.5 s / 33.6 s, build, vet, lint, test, race all exit 0 | 0 |
| `proxy.golang.org:443` | `setup[0]` failed | 1, `connect storage.googleapis.com:443` |
| `proxy.golang.org:443,storage.googleapis.com:443` | `setup[0]` passed, `setup[1]` failed verifying the module | 1, `connect sum.golang.org:443` |

The hosts this repository's setup needs are exactly these:

- `proxy.golang.org:443`: modules and the toolchain.
- `storage.googleapis.com:443`: the redirect target of some module zips
  and of the toolchain zip.
- `sum.golang.org:443`: checksum lookups for `go install`. `go mod
  download` needs none, because `go.sum` is present.

The measurer saw neither `dl.google.com` nor `golang.org` in six runs.

### Isolation (integration tests, real Engine)

- A gate running `wget http://example.com/...` fails by its exit code.
  The report carries one `http example.com:80` denial with no path.
- A gate sending `CONNECT example.com:443` fails, and the report carries
  one `connect example.com:443` denial. busybox `wget` ignores
  `https_proxy`, so the gate asks for the tunnel by hand, the way curl
  does.
- The workspace resolves and reaches `forja-gateway:3128` and a sibling on
  its run network by name.
- `nc 1.1.1.1 80` from the workspace fails, and `nslookup example.com`
  fails.
- With two runs in parallel, run A does not resolve run B's sibling and
  cannot reach B's gateway by its address. In A, `forja-gateway` resolves
  to A's own gateway only.
- After `Close`, the workspace, the gateway and the network are all
  NotFound. A launch that fails on a bad image, a bad tree or a missing
  gateway binary leaves no container or network labelled with its run.

## Consequences

- The pilot contract cannot pass setup until `setup[1]` gets
  `GOTOOLCHAIN=auto` or the image moves to `golang:1.26`. That is a change
  to `.forja.yaml`, outside this slice, and it holds with or without the
  gateway.
- For the pilot, the operator sets `FORJA_EGRESS_ALLOW` to
  `proxy.golang.org:443,sum.golang.org:443,storage.googleapis.com:443`.
  Every tunnel to those hosts is allowed and not logged.
- Each run costs one network, one small container and a copy of the
  forjad binary. Docker's default address pools hold a limited number of
  bridge networks, roughly 30 at a time. Parallelism beyond that needs
  `default-address-pools` configured on the node.

## Not covered: attempts that bypass the proxy

The `--internal` network denies, but does not record, any attempt that
ignores the proxy variables:

- raw sockets;
- IP literals;
- DNS lookups of external names.

The gateway cannot attribute a request to a process inside the workspace
either. The issue asks for process attribution "quando possível", and
here it is not possible. Logging these attempts, and attributing them to
a process, belongs to the hardened runtime (#23).

## Follow-ups

- Networks and gateways left by a control-plane crash are not recovered
  on start. They can be found by the `forja.run` label. No issue tracks
  this yet.
- The gateway's log goes to Docker's default log driver, which has no size
  cap. A workspace that floods denials grows that log for as long as the
  run lasts. The count must stay exact, so capping it needs the gateway
  to summarise rather than the daemon to rotate.
- Sidecars (#9) will join the run network and add their names to
  `NO_PROXY`. The model proxy (#11) and the registry proxy (#16) will
  either join the run network as siblings or be reached through the
  gateway's allowlist. That choice belongs to those slices.

## What only the human can do

- [ ] Ratify the gateway model, or choose differently: forjad as the
      proxy, an exact-match `host:port` allowlist, plain HTTP always
      refused, and non-public upstreams refused. On ratification, change
      this ADR's status to `accepted`.
- [ ] Decide who owns public module access: this operator allowlist, or a
      contract field.
- [ ] Fix the pilot contract's toolchain switch (`GOTOOLCHAIN=auto` in
      `setup[1]`, or `image: golang:1.26`).
- [ ] Measure the same scenarios on the GitHub Actions runner. They were
      measured only on colima.

## References

- `docs/PRD.pdf`, "Segurança e isolamento" and the metrics table.
- [ADR 0026](0026-workspace-isolation.md).
- `CLAUDE.md`, rules 3, 4 and 5.
