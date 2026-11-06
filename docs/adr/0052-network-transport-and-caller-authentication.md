# ADR 0052: network transport and caller authentication

- **Status:** accepted
- **Issue:** none yet (PR #52)
- **Refines:** [ADR 0051](0051-forja-as-a-separate-product.md) §5, which
  keeps stdio as the supported transport and defers a network transport with
  caller authentication to its own ADR. This is that ADR. It supersedes
  nothing.
- **Date:** 2026-11-05

## Context

`forjad serve` speaks MCP over stdio only (`cmd/forjad/main.go`). A stdio
client starts Forja as its own child process. That works for a client on a
developer's machine. It does not work for FuseOne:

- **In FuseOne, a stdio server runs inside the worker.** fuseone-agents
  NT-008 §4: a stdio server is "code executed inside the worker's container,
  and it inherits the worker's network position and credentials". Forja's
  control plane holds the Docker socket, the GitHub App private key and the
  model key. Starting it inside FuseOne's worker merges the two trust
  boundaries that ADR 0051 keeps apart, and puts workspaces on FuseOne's
  nodes instead of the dedicated pool of ADR 0026.
- **Several callers cannot share a child process.** Each stdio client gets
  its own `forjad` with its own run store and its own recovery state
  (`FORJA_STATE_DIR`). The claims in git are already shared; the runs are
  not.
  ADR 0051 §2 makes Forja callable by any MCP client, so one deployment has
  to serve more than one.
- **An HTTP server in FuseOne is an address with a credential.** FuseOne
  records a server as `http` with a URL and one credential kind: a bearer
  `Token`, exact `Headers`, an `OAuth` grant or a `GitHubApp`
  (`internal/domain/mcp_credentials.go` in fuseone-agents). Beside a GitHub
  App, plain `http://` is accepted only on loopback
  (`internal/githubapp/tls.go`), and fuseone-agents #252 reaches factory-mcp
  over HTTPS behind an internal ingress.

The workspace is not part of this question. It is ephemeral and never has an
address. The control plane creates it, reaches it through the Docker API, and
its only network is the run's `--internal` network to the proxies (ADRs
0026, 0035). What needs an address is the control plane, which lives as long
as the deployment does.

The SDK Forja already uses (`github.com/modelcontextprotocol/go-sdk` v1.8.0)
ships the pieces: `mcp.NewStreamableHTTPHandler` with a `Stateless` option,
and `auth.RequireBearerToken` with a `TokenVerifier` that puts a `TokenInfo`
on the request context. Forja makes no server-to-client request (no
elicitation, sampling or roots), so nothing it does needs a session.

## Decision

### 1. Streamable HTTP, stateless, beside stdio

`forjad serve` serves MCP over Streamable HTTP when `FORJA_MCP_ADDR` is set,
and over stdio when it is not. One process, one transport. The tools, their
schemas and their annotations are the same on both, so the change is not a
breaking change to the public contract of ADR 0051 §1.

The HTTP handler runs **stateless** (`StreamableHTTPOptions.Stateless`):
no `Mcp-Session-Id`, no session held in memory. A request can reach any
replica, a restart drops no session, and an ingress needs no sticky routing.
What a client needs to follow a run is the `run_id`, not a session.

The legacy HTTP+SSE transport is not served.

### 2. TLS or loopback, never plain HTTP on the network

`forjad` refuses to start when `FORJA_MCP_ADDR` names a non-loopback address
and no certificate is configured (`FORJA_TLS_CERT_FILE`,
`FORJA_TLS_KEY_FILE`). It fails loudly rather than serving a bearer in clear
text. The same rule FuseOne applies on its side.

Where TLS ends is a deployment choice, not a code path. Either way TLS goes
all the way to `forjad`:

- **Preferred: an internal ingress, with HTTPS to `forjad` as well.** Use it
  where the cluster already offers an ingress and certificate management.
  The ingress validates the backend's certificate, both the CA and the name
  (the Service's DNS name). Without that check, re-encryption does not
  authenticate the pod.
- **Fallback: clients call the Service directly** over a certificate from an
  internal CA they trust. Use it where that infrastructure does not exist.
  No ingress is built for the pilot alone.

### 3. A bearer per client, stored as a hash

Each caller is a **client**: a name and a bearer token. Forja reads its
clients from a file named by `FORJA_CLIENTS_FILE`, mounted from the vault:

```yaml
clients:
  - name: fuseone-pilot              # appears on runs and in logs; stable across rotations
    token_sha256: 3f1c…              # hex SHA-256 of the bearer; the bearer is not stored
    repositories: [acme/payments]    # owner/name, or owner/* for an owner's repositories
    rotation_owner: platform-team    # a team or role, never a person's name; not logged
```

- **One bearer per integration and environment.** FuseOne's pilot and
  FuseOne's staging are two clients with two bearers, never one shared
  bearer. A client in the pilot names its repositories explicitly, not with
  `owner/*`.

- The `TokenVerifier` hashes the presented bearer and compares it with
  `crypto/subtle.ConstantTimeCompare` against every entry. A miss is
  `auth.ErrInvalidToken`, answered `401` by the SDK's middleware.
- The verifier returns a `TokenInfo` with no expiration, so the middleware
  is configured with `RequireBearerTokenOptions.AllowMissingExpiration`. By
  default the SDK rejects a token without one; these bearers are static and
  rotate by restart, not by expiry.
- A bearer is at least 32 bytes from `crypto/rand`, encoded as base64url.
  An unsalted SHA-256 is safe only for a high-entropy secret: a leaked
  clients file must not let anyone brute-force the bearer.
- Rotation is two entries with the same `name` and different hashes, then
  removing the old one. The `name` never changes on a rotation, so runs and
  `forja.status` scoping carry over. The file is read at start; a change is
  a restart. `rotation_owner` says who performs it.
- The operator generates the bearer outside Forja and hands it to the
  client's credential store, for FuseOne the `Token` of its `http` server.
  Forja never sees the bearer at rest.
- With `FORJA_MCP_ADDR` set and no clients file, or an empty one, `forjad`
  refuses to start. There is no anonymous mode.

stdio has no clients file and no bearer: the process that started Forja is
the client, as today.

### 4. What a client may reach

Authentication says who the client is. Authorisation is narrower than the
GitHub App:

- **Repositories.** A tool call naming a repository outside the client's
  `repositories` is a tool error, refused before the contract is read or a
  claim is attempted. The App's installations stay the outer bound (ADR
  0051 §4): a repository in the list that the App cannot reach still fails
  where it fails today.
  - **Case.** Owners and names compare **without regard to ASCII case**,
    because GitHub's owner and repository names are case-insensitive:
    `Acme/Payments` is `acme/payments`, and a case-sensitive match would
    refuse a client the same repository it was given. Only ASCII letters,
    digits, `.`, `_` and `-` are accepted on either side, so Unicode case
    folding (the Kelvin sign folds to `k`) cannot make a different string
    match.
  - **Shape.** A requested repository is exactly one owner and one name. A
    pattern or a request whose owner or name is `.` or `..` is refused:
    the clients file refuses such a pattern at start, and the match refuses
    such a request whatever the patterns.
  - **stdio.** The stdio client is the fixed name `stdio` and reaches every
    repository of that shape, as before this ADR; the App's installations
    are its only bound. No entry of the clients file may take that name.
- **Runs.** Every run records the name of the client that started it.
  `forja.status` returns only the runs of the calling client; another
  client's `run_id` is answered as unknown, not as forbidden, so a client
  cannot learn that a run exists.
- **Tools.** Every client reaches all five tools. Which ones need a person's
  approval is the client's decision, from the annotations (ADR 0051 §4).

The claim in git (`refs/forja/claims/<n>`, ADR 0033) is still the one lock
per issue across every client and every replica.

#### What a response may name

A run is *the caller's* when the run store records the calling client as its
owner. A run the store does not know (a crash before the durable store, or
another replica's in-memory store) is treated as another client's: deny by
default.

| Response | When the run is the caller's | When it is another client's |
|---|---|---|
| `forja.status` with a `run_id` | The full record | The same tool error as an id that never existed, byte for byte |
| `forja.status` list | Included | Never included; the page size, the cursor and `next_cursor` behave as if the run did not exist |
| A claim refused because it is held (`domain.ClaimHeldError`) | Repository, issue, the holder's `run_id`, lease expiry | Repository, issue, lease expiry. No `run_id`, no client name |
| `supersedes` on the caller's run (the expired claim it took over) | The superseded `run_id` | Omitted, as if the issue had been free |
| Operator logs and the operator view (ADR 0049) | Client name, tool, `run_id` | The same: the operator sees every client |

Today `ClaimHeldError.Error()` always names the holder's run
(`internal/domain/claim.go`). The tool layer gets a caller-aware rendering;
the domain error keeps the holder for the control plane's own use.

**The limit of this confidentiality is the repository.** When two clients
reach the same repository, everything Forja writes *into the repository* is
visible to whoever can read it, whatever client they use: the claim ref and
its commit (run id, SHA, lease expiry, `internal/claim/file.go`), the branch,
the PR, the evidence report, which renders the run id
(`internal/evidence/render.go`), and the commit statuses. Forja does not hide
these, and could not. So a `run_id` is an identifier, not a secret, and not a
capability: knowing another client's `run_id` gets nothing through Forja's
tools. What Forja keeps per client is the run *record*: state, reason, cost,
tokens, egress and registry events, and the client's name. Two clients whose
work must stay mutually confidential must not share a repository.

### 5. The client on the run, not in the evidence

`domain.Run` gains the client's name. It is returned by `forja.status` and
shown in the operator view (ADR 0049). It is not written into the evidence
report or the commit statuses: those describe what ran on which SHA, and
their structure is part of the public contract.

Logs carry the client's name, the tool and the `run_id`. They never carry
the bearer, its hash, or a tool argument that holds free text, such as a
correction round's `finding`.

### 6. Request bounds

The handler caps a request body at 1 MiB
(`StreamableHTTPOptions.MaxRequestBodyBytes = 1 << 20`, answered `413`; the
SDK's default is 4 MiB). The largest legitimate input is a 64 KiB `finding`
(`internal/mcp/implement_round.go`). `ReadHeaderTimeout` and `ReadTimeout`
are set on the server; `WriteTimeout` is **not**. A tool call's own duration
is bounded by the run's `wall_clock` (ADR 0043), not by the listener.

While `forja.implement` blocks (until M2), a write timeout on the server or a
read timeout on the ingress drops the response, and with it the run, which
is cancelled (§7). The ingress's proxy read timeout is
set above the largest `limits.wall_clock` the deployment allows.

### 7. Protocol versions, and a caller that goes away

**The HTTP server accepts protocol version 2026-07-28 only.** stdio keeps the
SDK's full list. Until `forja.cancel` exists (roadmap, M2), a caller that
goes away must stop the work it started. 2026-07-28 makes the closing of the
request's stream the cancellation of that request (MCP specification,
2026-07-28, Streamable HTTP transport), and go-sdk v1.8.0 honours that only
with propagation turned on. Configuration:

- `ServerOptions.SupportedProtocolVersions = ["2026-07-28"]` on the HTTP
  server.
- `StreamableHTTPOptions.PropagateRequestCancellation = true`. In v1.8.0
  that option ties the tool handler's context to the HTTP request on
  2026-07-28 (`mcp/streamable.go`, `shouldPropagateCancellation`).
- A Forja middleware answers `400` to any POST whose `MCP-Protocol-Version`
  header is missing or is not `2026-07-28`. The SDK alone is not enough:
  it rejects an older header value, but it serves a request **without** the
  header on the pre-2026 path (as 2025-03-26), where the cancellation does
  not propagate. A 2026-07-28 client sends the header on every request
  (`mcp/streamable.go`, client side).

Why 2025-11-25 is refused rather than tolerated: in v1.8.0, on that version
the jsonrpc2 layer hides the request's cancellation from the handler
(`internal/jsonrpc2/conn.go`, `notDone`), and `session.Close` waits for the
handler to return. A `notifications/cancelled` cannot reach it either: in
stateless mode it arrives on another POST, in another ephemeral session. A
run there would outlive a dropped caller until its `wall_clock`.

When the caller goes away during `forja.implement` or `forja.verify`, the
run's context is cancelled. The workspace is torn down, and the claim is
released and the run recorded `failed` on the detached context
(`internal/mcp/lifecycle.go`). No claim and no run in `running` is left
behind.

A go-sdk v1.8.0 client pinned to an older version sends its `initialize`
without the header, so the middleware refuses that first request `400`.
Behind the middleware the SDK would answer it with `2025-11-25`, and the
client's next request, carrying that version, would be refused `400`. Either
way a client that cannot speak 2026-07-28 fails at connection time, not in
the middle of a run. Widening the list later is a decision for this ADR,
not a configuration change.

A stop of `forjad serve` cancels the calls in flight the same way: every
request's context ends with the server's (`http.Server.BaseContext`), and
the drain (4 min 15 s) outlasts a cancelled run's longest teardown plus a
15 s margin. The longest is a caller that leaves during the claim: eight
30 s steps one after the other, the ref mutation and the read-back that
settles an answer it lost (`claim.SettleBound`), then the six records and
the release the run still makes (`mcp.CancelledRunRecords` of
`FinishTimeout` each). A caller that leaves as the agent runs takes the
agent's two teardown steps, reading the proxies and removing the workspace,
then four records, which is shorter. The drain is derived from those
constants, and a test counts the steps against them. So a cancelled run
removes its workspace, releases its claim and is recorded `failed` before
the process exits. That holds only if the process is given the drain: the
pod's `terminationGracePeriodSeconds` (or the runtime's stop timeout) must
exceed the drain, 255 s; 270 s is the value to set. Kubernetes' default of
30 s does not, and neither does 230 s, which covered seven steps before the
read-back was added. There is no chart in `deploy/` yet; the value goes in
when it lands. A stop also cancels short read-only calls in
flight, and a run cancelled between its push and its pull request leaves
the pushed branch behind.

### 8. Origin validation

The MCP transport spec requires a server to validate `Origin` on every
incoming connection and to answer `403` when it is invalid; go-sdk v1.8.0
does not do this by default (`StreamableHTTPOptions.CrossOriginProtection` is
nil by default and deprecated). Forja wraps the handler itself:

- **Allowed origins** come from `FORJA_MCP_ALLOWED_ORIGINS`, a comma-separated
  list of exact `scheme://host[:port]` values, compared after lower-casing
  the scheme and host. No wildcards. The default is empty, and **the pilot
  keeps it empty**: requests without `Origin` are served once authenticated,
  and any `Origin` that is sent is refused. That serves the current worker
  with a `Token` credential.
- **No CORS.** Forja answers no preflight and sets no
  `Access-Control-*` header; an `OPTIONS` carrying an `Origin` is refused
  like any other. When a concrete browser client is needed, its origins and
  CORS support are decided together, as a change to this ADR.
- **A request with an `Origin` header** whose value is not in the list is
  answered `403 Forbidden`, with no MCP body and no `WWW-Authenticate`. That
  includes `Origin: null`. It is refused **before** the bearer is checked, so
  a browser page cannot use Forja's answers to probe a token.
- **A request without `Origin`** passes this check. Service clients such as
  FuseOne's worker send none, and the bearer still applies. A browser always
  sends `Origin` on a cross-origin POST, which is the case this check exists
  for.
- The SDK's localhost protection stays on (`DisableLocalhostProtection` is
  false), so a loopback listener still refuses a non-loopback `Host`.

`http.CrossOriginProtection` is not used: it trusts any `Origin` that
matches `Host`, exempts `GET`, `HEAD` and `OPTIONS`, and has no explicit
allowlist. A failed check is logged with the client address and the
`Origin`, never the bearer.

The handler stack, outermost first: Origin check, bearer check
(`auth.RequireBearerToken`), removal of the `Authorization` header, the
SDK's Streamable HTTP handler. Once the bearer is checked, the client it
names travels on the request's context; no tool handler can read the bearer
itself from the request's headers.

## Alternatives considered

| Alternative | Why not |
|---|---|
| Keep stdio and run `forjad` inside FuseOne's worker | Puts the Docker socket, the App key and the model key in the worker's container, and workspaces on FuseOne's nodes. It is the absorption ADR 0051 rejects, done by packaging. |
| The role App's installation token as the bearer, checked against GitHub (the factory-mcp pass-through of fuseone-agents #252) | Gives a per-repository authorisation for free, but every call hands Forja a token that can write to the repository as the role. Forja would hold a credential it must never use, and only FuseOne could be a client. |
| OAuth 2.1, as in the MCP authorization spec | Needs an authorisation server and token lifetimes for a caller that is a service, not a person. Worth it when people call Forja from their own clients; the `TokenVerifier` seam stays the place to add it. |
| Mutual TLS | FuseOne's credential kinds have no client certificate, so its first client could not use it. |
| No caller authentication, a NetworkPolicy only | Through an ingress, the policy sees the ingress controller, not the caller (fuseone-agents #252, open points). Every caller in the network would reach every repository and every run. |
| A stateful HTTP session | Ties a client to a replica and loses every session on a restart, for state Forja does not use. |

## Consequences

- `cmd/forjad` gains the HTTP path: configuration, TLS, the clients file and
  the verifier. The verifier and the repository match are pure functions with
  their own tests; matching `owner/*` is a property-test candidate.
- `domain.Run` and the run store carry the client's name; `forja.status`
  filters by it. On stdio the client is a fixed name, so the filter never
  hides a run from the process that started it.
- A blocking `forja.implement` over HTTP lasts as long as the run. A client
  that goes away stops it, because HTTP accepts 2026-07-28 only (§7); the
  claim release and the terminal record run on a context detached from the
  caller's (`internal/mcp/lifecycle.go`), so nothing is left behind.
- A client that speaks only 2025-11-25 or older cannot use Forja over HTTP.
  It can over stdio. Making runs outlive the request — `run_id` at start, outcome
  in `forja.status`, a `forja.cancel`, an idempotency key — changes the tool
  schemas and gets its own ADR, before the first tagged major
  (`docs/roadmap.md`, M2).
- The tool layer renders a held claim and `supersedes` according to who owns
  the run (§4); `domain.ClaimHeldError` itself is unchanged.
- The in-memory run store answers `forja.status` only for the replica and
  the process that ran the run. Several replicas, or a restart that keeps
  history, need the durable run store (roadmap, M1). Until then the chart
  runs one replica.
- FuseOne registers Forja as an `http` server with a `Token` credential and a
  URL from the installation's configuration. Issue #15 is rewritten for that.
- `forjad` gains `/healthz` and `/readyz` on the metrics listener
  (`FORJA_METRICS_ADDR`), which today serves only `/metrics`. They are
  unauthenticated and answer nothing about runs.

## Acceptance criteria

Each is observable from outside `forjad`: an SDK client (`mcp.ClientOptions.
ProtocolVersion` pins the version) or plain HTTP against a real listener.

| # | Scenario | Observable result |
|---|---|---|
| A1 | Bearer whose hash is in the clients file, no expiration | `tools/list` returns the five tools |
| A2 | Missing or unknown bearer | `401`, no tool runs |
| A3 | Non-loopback `FORJA_MCP_ADDR` without a certificate, or with no or an empty clients file | `forjad` exits non-zero at start, naming the variable |
| A4 | Request body over 1 MiB | `413` |
| C1 | Client on 2026-07-28 closes the connection during `forja.implement` | The workspace container is gone; `refs/forja/claims/<n>` is deleted within the release bound (`FinishTimeout`, 30 s per step, `internal/mcp/implement.go`); and `forja.status` (new connection, same client) shows the run `failed` |
| C2 | Client pinned to 2025-11-25 | Refused at connection time (`400` on its `initialize`, which carries no `MCP-Protocol-Version`); no tool runs, no claim ref created |
| C3 | `MCP-Protocol-Version: 2025-06-18` or `2025-11-25` on any request | `400` |
| C4 | POST with a valid bearer and no `MCP-Protocol-Version` header | `400`; no tool runs |
| P1 | Client B calls `forja.status` with client A's `run_id` | The error is identical to that for a random id |
| P2 | Client B lists runs of a repository both reach | None of A's runs appear; pagination is unaffected |
| P3 | Client B calls `forja.implement` on an issue A's run holds | A tool error naming repository, issue and expiry; the text contains no `run_id` |
| P4 | Client A retries the same issue while its own run holds it | The error names A's `run_id` |
| P5 | B's run takes over A's expired claim | B's `forja.status` has no `supersedes`; A's run id appears in neither the response nor B's logs |
| O1 | `Origin` not in `FORJA_MCP_ALLOWED_ORIGINS`, valid bearer | `403`, no tool runs, no `WWW-Authenticate` |
| O2 | `Origin: null`, valid bearer | `403` |
| O3 | No `Origin`, valid bearer | `200` |
| O4 | `Origin` in the list, valid bearer (test configuration only; the pilot's list is empty) | `200` |
| O5 | `Origin` not in the list, no bearer | `403` (the Origin check runs first) |
| O6 | `OPTIONS` preflight with an `Origin` | `403`; no `Access-Control-*` header |

### Pilot release gate

The ADR can be ratified on the design. **The pilot is released only after**
a test in the pilot's environment, with fuseone-agents' real worker through
the real ingress, shows all of the following:

| # | In the pilot's environment | Observable result |
|---|---|---|
| G1 | Negotiation | The worker's Forja entry is `auto` and is not overridden by a recorded mode (`withProtocol`); requests carry `MCP-Protocol-Version: 2026-07-28` |
| G2 | Authentication | `tools/list` succeeds with the pilot's bearer (A1) |
| G3 | Refusal without a bearer | `401` (A2) |
| G4 | Invalid origin | `403` (O1) |
| G5 | Another client's run | A second, test-only client gets P1 and P3 behaviour |
| G6 | Cancellation | The worker drops a `forja.implement` mid-run: the workspace is removed, the claim ref is deleted, and `forja.status` shows a terminal state (C1) |
| G7 | TLS | The ingress rejects a backend certificate with the wrong name or from an untrusted CA (§2, preferred path) |

## Maintainer decisions (2026-11-05)

Recorded from the maintainer's review of PR #52. They are written into the
Decision sections above.

1. **Protocol:** the pilot's HTTP server accepts 2026-07-28 only (§7).
   FuseOne's Forja entry stays `auto`. Evidence, fuseone-agents at
   `f1fa871` (0.91.0): `auto` uses go-sdk v1.7.0's default client version,
   2026-07-28 (`cmd/agentd/mcp.go`, `transportFor`); `legacy` is opt-in.
2. **Origins:** `FORJA_MCP_ALLOWED_ORIGINS` is empty (§8). Evidence, same
   commit: the `Token` transport sets only `Authorization`
   (`bearer.RoundTrip`). Browser origins and CORS are decided when a
   concrete need appears.
3. **Run ids in shared repositories:** accepted (§4). Whoever reads the
   repository already reads its branches, PRs and evidence. A `run_id`
   grants no access to another client's records. Clients that need
   confidentiality about their work use separate repositories.
4. **TLS:** an internal ingress with HTTPS to `forjad`, validating the
   backend's CA and name, where the cluster offers ingress and certificate
   management; otherwise the Service directly with an internal CA (§2).
5. **Bearer:** one per integration and environment, 32 random bytes, stored
   in FuseOne's credential store, only its SHA-256 mounted into Forja
   through the vault. The pilot names its repository explicitly. A
   rotation owner is recorded, and the client name is kept across rotations
   (§3).
6. **Ratification and release:** the design can be ratified to guide the
   implementation. The pilot is released only after the release gate
   passes (G1–G7).

## What only the human can do

- [x] Ratify this ADR and change its status to `accepted` (2026-11-05).
- [ ] Check that `withProtocol` applies no recorded mode to FuseOne's Forja
      entry, so it stays `auto` (G1).
- [ ] Find out whether the pilot's cluster offers an internal ingress and
      certificate management, and choose the TLS path of §2 accordingly.
- [ ] Generate the pilot's bearer, store it in FuseOne's `Token` credential,
      mount its SHA-256 into the clients file through the vault, and name
      the pilot repository and the `rotation_owner` in its entry.
- [ ] Run the pilot release gate (G1–G7) in the pilot's environment before
      the pilot is released.

## References

- [ADR 0026](0026-workspace-isolation.md), [ADR 0033](0033-reservation-by-ref-with-lease.md),
  [ADR 0035](0035-egress-gateway.md), [ADR 0043](0043-run-limits.md),
  [ADR 0049](0049-operator-view.md), [ADR 0051](0051-forja-as-a-separate-product.md).
- [Roadmap](../roadmap.md), M1 and M2.
- fuseone-agents: `docs/NT-008-a-catalogue-by-shape.md` §4,
  `internal/domain/mcp_credentials.go`, `internal/githubapp/tls.go`,
  issue #252; at commit `f1fa871` (0.91.0), `cmd/agentd/mcp.go`
  (`transportFor`, `bearer.RoundTrip`, `withProtocol`).
- MCP Streamable HTTP transport; `go-sdk` v1.8.0 `mcp/streamable.go` and
  `auth/auth.go`.
