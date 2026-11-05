# ADR 0052: network transport and caller authentication

- **Status:** proposed
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

Where TLS ends is a deployment choice, not a code path: behind an internal
ingress that re-encrypts to the pod, or with clients calling the Service
directly over a certificate from an internal CA. Both keep TLS up to `forjad`.

### 3. A bearer per client, stored as a hash

Each caller is a **client**: a name and a bearer token. Forja reads its
clients from a file named by `FORJA_CLIENTS_FILE`, mounted from the vault:

```yaml
clients:
  - name: fuseone-pilot              # appears on runs and in logs
    token_sha256: 3f1c…              # hex SHA-256 of the bearer; the bearer is not stored
    repositories: [acme/payments]    # owner/name, or owner/* for an owner's repositories
```

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
  removing the old one. The file is read at start; a change is a restart.
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
read timeout on the ingress drops the response. What happens to the run then
depends on the protocol version (§7). The ingress's proxy read timeout is
set above the largest `limits.wall_clock` the deployment allows.

### 7. Protocol versions, and a caller that goes away

The HTTP server negotiates exactly two protocol versions:
`ServerOptions.SupportedProtocolVersions = ["2026-07-28", "2025-11-25"]`.
stdio keeps the SDK's full list. In go-sdk v1.8.0, a request whose
`MCP-Protocol-Version` header names an older version is answered `400`. A
legacy `initialize` asking for an older version is answered with
`2025-11-25`, and the client either speaks it or disconnects.

The handler sets `StreamableHTTPOptions.PropagateRequestCancellation = true`.
In v1.8.0 that option ties the tool handler's context to the HTTP request
**only on 2026-07-28** (`mcp/streamable.go`, `shouldPropagateCancellation`).
On 2025-11-25 the jsonrpc2 layer hides the request's cancellation from the
handler (`internal/jsonrpc2/conn.go`, `notDone`), and `session.Close` waits
for the handler to return. A `notifications/cancelled` cannot reach it
either: in stateless mode it arrives on another POST, in another ephemeral
session. So:

| Version | The caller goes away during `forja.implement` or `forja.verify` |
|---|---|
| 2026-07-28 | The run's context is cancelled. The workspace is torn down, the claim released and the run recorded `failed` on the detached context (`internal/mcp/lifecycle.go`) |
| 2025-11-25, or a request with no version header | The run continues to its end or its `wall_clock`. The response is lost, the claim is released at the end, and the outcome is in `forja.status` |

Neither row leaves a claim or a run in `running` behind. On 2025-11-25,
nothing short of `wall_clock` stops a run until `forja.cancel` exists
(roadmap, M2). A caller that retries meanwhile gets a held claim naming its
own run (§4) and can follow that run in `forja.status`.

### 8. Origin validation

The MCP transport spec requires a server to validate `Origin` on every
incoming connection and to answer `403` when it is invalid; go-sdk v1.8.0
does not do this by default (`StreamableHTTPOptions.CrossOriginProtection` is
nil by default and deprecated). Forja wraps the handler itself:

- **Allowed origins** come from `FORJA_MCP_ALLOWED_ORIGINS`, a comma-separated
  list of exact `scheme://host[:port]` values, compared after lower-casing
  the scheme and host. No wildcards. The default is empty.
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
(`auth.RequireBearerToken`), the SDK's Streamable HTTP handler.

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
- A blocking `forja.implement` over HTTP lasts as long as the run. Whether
  a client that goes away stops it depends on the protocol version (§7);
  either way the claim release and the terminal record run on a context
  detached from the caller's (`internal/mcp/lifecycle.go`), so nothing is
  left behind. Making runs outlive the request — `run_id` at start, outcome
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
| C1 | Client on 2026-07-28 closes the connection during `forja.implement` | The workspace container is gone; `refs/forja/claims/<n>` is deleted within the release bound (`finishTimeout`, 30 s per step, `internal/mcp/implement.go`); and `forja.status` (new connection, same client) shows the run `failed` |
| C2 | Client on 2025-11-25 closes the connection during `forja.implement` | The run reaches its own end; then the claim ref is deleted and `forja.status` shows its terminal state. Never `running` after `wall_clock` plus the lease margin |
| C3 | `MCP-Protocol-Version: 2025-06-18` on a non-initialize request | `400` |
| P1 | Client B calls `forja.status` with client A's `run_id` | The error is identical to that for a random id |
| P2 | Client B lists runs of a repository both reach | None of A's runs appear; pagination is unaffected |
| P3 | Client B calls `forja.implement` on an issue A's run holds | A tool error naming repository, issue and expiry; the text contains no `run_id` |
| P4 | Client A retries the same issue while its own run holds it | The error names A's `run_id` |
| P5 | B's run takes over A's expired claim | B's `forja.status` has no `supersedes`; A's run id appears in neither the response nor B's logs |
| O1 | `Origin` not in `FORJA_MCP_ALLOWED_ORIGINS`, valid bearer | `403`, no tool runs, no `WWW-Authenticate` |
| O2 | `Origin: null`, valid bearer | `403` |
| O3 | No `Origin`, valid bearer | `200` |
| O4 | `Origin` in the list, valid bearer | `200` |
| O5 | `Origin` not in the list, no bearer | `403` (the Origin check runs first) |

## What only the human can do

- [ ] Ratify this ADR and change its status to `accepted`.
- [ ] Decide the protocol versions the pilot's HTTP server accepts (§7).
      **Evidence**, fuseone-agents at `f1fa871` (0.91.0): an `http` server in
      the default `auto` protocol mode uses go-sdk v1.7.0's default client
      version, which is 2026-07-28 (`cmd/agentd/mcp.go`, `transportFor`;
      `legacy` is opt-in per server). Caveat: the reconciler can replace
      `auto` with a mode recorded for that server (`withProtocol`), so the
      Forja entry must stay `auto`.
      **Recommendation, not yet decided:** the pilot's HTTP server accepts
      only `["2026-07-28"]`, on condition that integration test C1 passes
      against fuseone-agents' real client. This avoids accepting runs that
      outlive a dropped caller on 2025-11-25.
- [ ] Decide the pilot's `FORJA_MCP_ALLOWED_ORIGINS` (§8).
      **Evidence**, same commit: the `Token` credential's transport sets
      only `Authorization` (`cmd/agentd/mcp.go`, `bearer.RoundTrip`). No
      code path in `cmd/agentd` sets `Origin`. A `Headers` credential sends
      whatever headers the operator configures, so it could carry one.
      **Recommendation, not yet decided:** keep the list empty while no
      browser-based client is needed.
- [ ] Integration test in the pilot's environment, still pending: fuseone-agents'
      worker, through the real ingress, negotiates 2026-07-28 with Forja,
      sends no `Origin`, and passes A1, C1 and O3. The code evidence above
      does not replace this test.
- [ ] Decide whether repository-level visibility of run ids (claim refs,
      evidence report) is acceptable for repositories shared between
      clients (§4).
      **Recommendation, not yet decided:** accept it, keeping per-client
      authorisation on every query. Knowing a `run_id` grants nothing.
- [ ] Decide where TLS ends in the pilot's cluster: internal ingress that
      re-encrypts, or the Service with an internal CA that FuseOne trusts.
- [ ] Generate FuseOne's bearer, put its hash in the clients file through the
      vault, and store the bearer in FuseOne's `Token` credential.
- [ ] Confirm with the fuseone-agents maintainers that their MCP client talks
      to a stateless Streamable HTTP server (no `Mcp-Session-Id`). Their
      client disables the standalone SSE stream (`DisableStandaloneSSE`),
      which suits a stateless server; the integration test above settles it.

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
