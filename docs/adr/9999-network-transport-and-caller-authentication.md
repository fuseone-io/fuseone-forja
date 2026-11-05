# ADR 9999: network transport and caller authentication

- **Status:** proposed
- **Issue:** none yet
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
  its own `forjad` with its own run store and its own view of the claims.
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

### 5. The client on the run, not in the evidence

`domain.Run` gains the client's name. It is returned by `forja.status` and
shown in the operator view (ADR 0049). It is not written into the evidence
report or the commit statuses: those describe what ran on which SHA, and
their structure is part of the public contract.

Logs carry the client's name, the tool and the `run_id`. They never carry
the bearer, its hash, or a tool argument that holds free text, such as a
correction round's `finding`.

### 6. Request bounds

The HTTP listener caps a request body at 1 MiB, the largest legitimate
input being a 64 KiB `finding`. Read and header timeouts are set on the
server. A tool call's own duration is bounded by the run's `wall_clock`
(ADR 0043), not by the listener.

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
- A blocking `forja.implement` over HTTP lasts as long as the run. The
  work runs on the request's context, as on stdio, so a client that goes
  away stops it; the claim release and the terminal record run on a context
  detached from the caller's (`internal/mcp/lifecycle.go`), so nothing is
  left behind. Making runs
  outlive the request — `run_id` at start, outcome in `forja.status`, a
  `forja.cancel` — changes the tool schemas and gets its own ADR, before the
  first tagged major (`docs/roadmap.md`, M2).
- The in-memory run store answers `forja.status` only for the replica and
  the process that ran the run. Several replicas, or a restart that keeps
  history, need the durable run store (roadmap, M1). Until then the chart
  runs one replica.
- FuseOne registers Forja as an `http` server with a `Token` credential and a
  URL from the installation's configuration. Issue #15 is rewritten for that.
- Health and readiness stay on the metrics listener (`FORJA_METRICS_ADDR`),
  unauthenticated and answering nothing about runs.

## What only the human can do

- [ ] Ratify this ADR and change its status to `accepted`.
- [ ] Decide where TLS ends in the pilot's cluster: internal ingress that
      re-encrypts, or the Service with an internal CA that FuseOne trusts.
- [ ] Generate FuseOne's bearer, put its hash in the clients file through the
      vault, and store the bearer in FuseOne's `Token` credential.
- [ ] Confirm with the fuseone-agents maintainers that their MCP client talks
      to a stateless Streamable HTTP server (no `Mcp-Session-Id`).

## References

- [ADR 0026](0026-workspace-isolation.md), [ADR 0033](0033-reservation-by-ref-with-lease.md),
  [ADR 0035](0035-egress-gateway.md), [ADR 0043](0043-run-limits.md),
  [ADR 0049](0049-operator-view.md), [ADR 0051](0051-forja-as-a-separate-product.md).
- [Roadmap](../roadmap.md), M1 and M2.
- fuseone-agents: `docs/NT-008-a-catalogue-by-shape.md` §4,
  `internal/domain/mcp_credentials.go`, `internal/githubapp/tls.go`,
  issue #252.
- MCP Streamable HTTP transport; `go-sdk` v1.8.0 `mcp/streamable.go` and
  `auth/auth.go`.
