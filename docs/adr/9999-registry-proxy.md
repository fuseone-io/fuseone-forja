# ADR 9999: per-run registry proxy

- **Status:** proposed (default taken by the team; awaits human ratification)
- **Issue:** #16
- **Refines:** [ADR 0026](0026-workspace-isolation.md), section "Egress";
  [ADR 0035](0035-egress-gateway.md), section "Follow-ups"
- **Date:** 2026-11-02

## Context

The PRD ("Contrato do repositório" and "Segurança e isolamento") says
`registries` lists where `setup` fetches private dependencies. The read
credential stays in a proxy, with one credential per area. The workspace
sees only the proxy, and `forja.doctor` refuses a repository whose setup
fails with its declared registries.

Before this decision the contract parser read `registries` as a plain list
of strings, and nothing else read it. `gates.Launcher.Launch` could not
pass a run's registries to the workspace. ADR 0035 left this slice to choose
between a sibling container on the run network and the gateway allowlist.
ADR 0027 records that the pilot has no private registry (`registries: []`).

## Decision

### 1. One sibling container per run, not the gateway allowlist

A run whose contract declares registries gets one registry proxy. It runs
the same forjad binary as the gateway, as a new subcommand:

```
forjad registry -run <id> -registries <e1,e2> -credentials /run/forja/registries.json
```

- Image `alpine:3.20`, user `65534`, `CapDrop: ALL`, `no-new-privileges`.
  Its environment is the image's own, with nothing from the control plane.
- Name `forja-run-<id>-registry`, label `forja.run=<id>`. On the run
  network it has the alias `forja-registry`, and it is also attached to
  `bridge`.
- A run with `registries: []`, such as the pilot, gets no extra container.
- Launch starts it after the gateway and before the workspace, so the
  workspace is created with its final environment. It waits at most 10 s
  for the readiness line. Any failure removes everything of the run.
- `Close` removes the sidecars, the workspace, the registry proxy, the
  gateway and the network, in that order, and stays idempotent.

The gateway is not used for this because it never terminates TLS, so it
cannot add a credential to a request. Keeping the credential holder apart
from the gateway also keeps the gateway's log format owned by `egress`
alone.

### 2. A plain-HTTP reverse proxy with a path prefix, no TLS interception

The workspace calls `http://forja-registry:8080/<entry>/<rest>`. The proxy
forwards it to `https://<entry>/<rest>` and keeps the query as sent.

- `<entry>` is the longest declared entry that matches on a segment
  boundary, so `ghcr.io/my-org` does not match `ghcr.io/my-org-evil`.
- Before matching, the proxy refuses a path with a `.` or `..` segment
  (also when percent-encoded), an encoded slash (`%2f`), or a backslash
  (raw or `%5c`).
- It sets `Authorization: Basic` from the entry's credential. It drops the
  client's `Authorization`, `Proxy-Authorization` and `Cookie`, and the
  hop-by-hop headers.
- It drops `Set-Cookie` from the answer: a session the upstream opens for
  the area's credential would be a credential in the workspace.
- Every upstream connection goes through `egress.DialPublic`, redirects
  included.
- The proxy follows redirects itself, at most 10, and only to `https`. It
  strips the credential from any hop whose `host:port` differs from the
  first request's. The standard library strips it only across domains, so
  a subdomain or another port of the same host would still receive it.
- A redirect to a non-public address, or away from `https`, is a denial.
- An upstream that cannot be reached is a 502 logged as `allowed`: the
  policy let the request through.
- The answer streams as the upstream sent it. The proxy never decompresses
  it and never parses it.
- A request outside every declared entry gets a 403 and is logged as
  denied.
- Methods are not restricted. Git's smart HTTP fetch needs `POST`, and a
  read-only credential is what keeps a request from writing.

### 3. Forja guesses no ecosystem

The PRD says "a Forja não adivinha nada". When registries are declared,
the workspace gets these two changes, and nothing else:

```
FORJA_REGISTRY_URL=http://forja-registry:8080
NO_PROXY=localhost,127.0.0.1,::1,forja-registry   no_proxy=(same)
```

A URL is an address, not a credential. The repository's `setup` points its
own tool at it, for example:

- Go: `GOPROXY=$FORJA_REGISTRY_URL/proxy.golang.org`, and
  `GOSUMDB="sum.golang.org $FORJA_REGISTRY_URL/sum.golang.org"` when
  `sum.golang.org` is declared too.
- Git (and `go get` of a private module):
  `git config --global url."$FORJA_REGISTRY_URL/github.com/my-org/".insteadOf "https://github.com/my-org/"`.
- npm: `npm config set registry "$FORJA_REGISTRY_URL/npm.pkg.github.com/"`
  with `replace-registry-host=always`.

busybox `wget` ignores `no_proxy`; it needs `-Y off`. Go, git and npm
honour `NO_PROXY`.

### 4. One credential per area, from the operator

`FORJA_REGISTRY_CREDENTIALS` is the path to a JSON file mounted from the
vault:

```json
{"<entry>": {"username": "...", "password": "..."}}
```

- `forjad serve` reads and validates it at startup. A bad file is a startup
  error that names the variable and never quotes the file: the decoder's
  own messages are replaced with byte offsets. Unset, there are no
  credentials.
- Every key must be a valid entry. Matching is exact on the entry.
- A declared registry with no credential makes `Launch` fail with
  `ErrRegistryCredentialMissing`, naming the entry, before anything is
  created.
- Only the run's declared entries are written into its proxy. They go to
  `/run/forja/registries.json` with `CopyToContainer` before start, mode
  `0400`, owner `65534:65534`, in a `0500` directory. They never go in
  `Config.Env` or `Cmd`, which `docker inspect` shows.
- `registry.Credential` prints as `registry.Credential{redacted}` under
  every `fmt` verb and marshals to JSON as that same string. Only
  `EncodeCredentials` writes the file form.

### 5. The access log is evidence, written like the gateway's

Each request produces one JSON line on the proxy's stdout:

```json
{"event":"access","run":"<id>","at":"...","registry":"<entry>","path":"/<rest>","outcome":"allowed","status":200}
```

- `path` has no query, and `path` and `registry` are each cut to 255 bytes.
- On a denial, `registry` is the first segment requested.
- A readiness line comes before serving.
- The line of a forwarded request is written before the answer goes out.
  If a line cannot be written, the request gets a 502 and the proxy stops.
- The clock is injected; `time.Now` is called only in `cmd/forjad`.
- `Container.Registry(ctx)` reads the log as strictly as `Container.Egress`.
  It refuses the whole log on an unknown line, on a line from another run,
  or when the container is no longer running. With no registry container
  it returns an empty report.
- `domain.RegistryReport{Accesses, Count}` keeps an exact count and lists
  the first 100. `GateReport.Registry` and `Run.Registry` carry it.
- The gates runner reads it after the egress, on the same detached
  context, before `Close`. A log that cannot be read is a run error.
- `forja.status` exposes `registry_access_count` and `registry_access`
  (`[{at, registry, path, outcome, status}]`). Both are always on the wire,
  and the list is empty when there is nothing.

### 6. The entry grammar lives in `internal/domain`

`domain.ValidRegistry` and `domain.MatchRegistry` are shared by the
contract parser, the credentials file and the proxy. An entry is
`host[:port][/segment...]`, lowercase. It has no scheme, query, fragment,
userinfo, wildcard, trailing slash, empty segment or dot segment. Segments
use `a-z 0-9 - . _ ~`. The parser lowercases each entry, then reports a bad
`registries[i]` as a contract error with its path and line. The error never
repeats the entry, so a secret pasted in its place is not printed.

## Measurements

Environment: local Docker on colima, server 29.5.2 linux/arm64,
`alpine:3.20`, `golang:1.25`, with the forjad binary cross-compiled from
this branch.

### Pilot setup through the registry proxy

`FORJA_PILOT_TEST=1 go test ./internal/workspace -run TestPilot_setupThroughRegistryProxy`
reads `.forja.yaml` without changing it. In memory it declares
`registries: [proxy.golang.org, sum.golang.org]` and prefixes both setup
steps with `GOPROXY=$FORJA_REGISTRY_URL/proxy.golang.org` and
`GOSUMDB="sum.golang.org $FORJA_REGISTRY_URL/sum.golang.org"`. `setup[1]`
also gets `GOTOOLCHAIN=auto`, as ADR 0035 measured. The egress allowlist is
empty, and both entries get a random canary credential.

| Declared | Result | Accesses | Egress denials |
|---|---|---|---|
| `proxy.golang.org` only | `setup[0]` passed (5.6 s); `setup[1]` failed, exit 1: `verifying module: Get "https://sum.golang.org/lookup/...": Forbidden` | 102; of the first 100 listed, proxy.golang.org 91 × 200 and 9 × 404 | 1, `connect sum.golang.org:443` |
| `proxy.golang.org`, `sum.golang.org` | `setup[0]` passed (2.3 s); `setup[1]` passed (58.5 s) | 1360; of the first 100 listed, proxy.golang.org 91 × 200 and 5 × 404, sum.golang.org 4 × 200 | 0 |

`https://proxy.golang.org/sumdb/sum.golang.org/supported` answers 404, so
the go command does not reach the checksum database through `GOPROXY`. It
has to be declared and pointed at with `GOSUMDB`. The 404s are the go
command probing module paths that do not exist, which is normal. Both
upstreams accepted the canary Basic credential. The measurer also found
that `storage.googleapis.com` answers 401 to an unrecognised Basic header.
That is why a cross-host redirect must lose the credential. No such
redirect showed in the measurer's runs.

### Isolation (integration tests, real Engine)

- The workspace resolves `forja-registry` and reaches port 8080. Its
  environment carries `FORJA_REGISTRY_URL` and `forja-registry` in both
  `NO_PROXY` spellings. A run with no registries has neither, and no
  registry container.
- The proxy runs as `65534` with `CapDrop: ALL` and `no-new-privileges`,
  on `bridge` and its run network. The credentials file is `0400`
  `65534:65534`.
- `wget $FORJA_REGISTRY_URL/example.com/x?token=…` from the workspace exits
  non-zero. `Container.Registry` reports exactly one access:
  `example.com /x denied 403`, with no query. Through the real gates runner
  the same access is in `GateReport.Registry`.
- With a random canary as the area's password:
  - `env` in the workspace does not show it;
  - `grep -r -F` over every directory but `/proc`, `/sys` and `/dev` does
    not find it;
  - `docker inspect` of the proxy shows it in neither `Env`, `Cmd` nor
    `Args`;
  - neither the commands' streams nor the proxy's raw log contain it.
- A declared entry with no credential refuses the launch, and nothing
  labelled with the run is left. A killed proxy makes its log refused.
  `Close` removes the proxy and is idempotent.

## Consequences

- An area onboards a private registry by declaring its entry in the
  contract and pointing its setup's tool at `$FORJA_REGISTRY_URL`. The
  operator adds the area's read credential to the vault file. A contract
  that declares an entry the operator has no credential for fails every
  run at launch, so `forja.doctor` says so before any agent runs.
- npm scoped packages request metadata as `/@scope%2fname`. The proxy
  refuses an encoded slash, so scoped npm metadata does not pass yet.
  Tarballs (`/@scope/name/-/...`) do pass.
- Entries are lowercase. A registry whose paths are case-sensitive has
  to be requested in lowercase.
- Each run that declares registries costs one more small container and a
  copy of the forjad binary. It costs no extra network.

## Not covered

- **OCI registries (ghcr.io).** The Bearer-token challenge
  (`WWW-Authenticate: Bearer realm=…`) is not handled inside the proxy,
  and neither is `/v2/<org>/` scoping. The `/v2/` prefix does not fit
  path-prefix matching, and handing the token to the client would put a
  credential in the workspace. This is a follow-up; no issue is open.
- **Ecosystem auto-configuration** (writing `.npmrc`, git config or
  `GOPROXY` for the repository) belongs to the repository's setup.
- **Inbound from the default bridge.** The proxy listens on every
  interface, so any container on the node's default bridge could reach
  it. Forja's own gateways and registry proxies cannot reach it: they
  only dial public addresses. An operator container on that bridge could.
  Binding the listener to the run network, or checking the client's
  address against the run network's subnet, is a follow-up.
- Vault integration beyond reading a file path, and credential rotation.
- Containers left by a control-plane crash, as in ADR 0035.
- The proxy's log, like the gateway's, goes to Docker's default log driver
  with no size cap.

## What only the human can do

- [ ] Ratify the model: a sibling proxy per run, plain HTTP with a path
      prefix, one credential per area from a vault file, no OCI support
      yet. On ratification, change this ADR's status to `accepted`.
- [ ] Provision a real private dependency and its area's read credential
      for the pilot. Then run `forja.doctor` with a setup that downloads it
      through a declared registry. The measurer found none today: no
      fuseone-io private Go module declares its own path, and the org's
      packages could not be listed without `read:packages`.
- [ ] Decide whether OCI registries (ghcr.io) need support, and open that
      issue.
- [ ] Decide whether `sum.golang.org` and `proxy.golang.org` go through the
      registry proxy or stay on the gateway allowlist (ADR 0035).

## References

- `docs/PRD.pdf`, "Contrato do repositório" and "Segurança e isolamento".
- [ADR 0026](0026-workspace-isolation.md), [ADR 0027](0027-pilot-repository.md),
  [ADR 0035](0035-egress-gateway.md).
- `CLAUDE.md`, rules 2, 3, 4 and 5.
