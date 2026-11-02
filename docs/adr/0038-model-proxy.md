# ADR 0038: model proxy and the agent in the workspace

- **Status:** proposed (default taken by the team; awaits human ratification)
- **Issue:** #11
- **Refines:** [ADR 0029](0029-workspace-agent.md) and
  [ADR 0035](0035-egress-gateway.md). It supersedes neither.
- **Date:** 2026-11-02

## Context

The PRD ("Segurança e isolamento") puts the model API key in the control
plane: the agent reaches the model through a proxy there, which holds the
key, applies the token cap and measures the run's cost. Step 4 of
`forja.implement` runs the agent with the issue and the `agent_context`; it
reaches the model only through the control plane's proxies and commits
locally.

ADR 0029 chose Claude Code headless (`claude -p --bare`) and fixed what the
proxy must do: swap `x-api-key`, stream SSE event by event, meter
`message_start` and `message_delta`, and refuse the cap with a 429 without
calling upstream. ADR 0035 left this slice to choose whether the model proxy
is a sibling on the run network or is reached through the gateway's
allowlist.

Before this decision `internal/proxy/model` and `internal/agent` did not
exist, `domain.Run` had no token field, and `forja.implement` claimed,
recorded and released with no work in between.

## Decision

### 1. One model proxy per agent launch, a sibling on the run network

The proxy is a container named `forja-run-<id>-model` with the alias
`forja-model` on the run network. It copies the gateway of ADR 0035:

- image `alpine:3.20`, with the control plane's own static linux forjad
  copied in by `CopyToContainer`, as `forjad model`;
- user `65534`, `CapDrop: ALL`, `no-new-privileges`;
- always attached to `bridge`, because its upstream is a fixed outside host;
- the launch waits at most 10 s for its readiness line.

Only `Docker.LaunchAgent` creates it. The plain `Launch` that `forja.doctor`
and the gates runner use never gets one, so a gate command cannot spend the
operator's key.

### 2. The real key

`forjad serve` reads `FORJA_MODEL_API_KEY` into memory. The driver copies it
into the model container as `/forja/key`, owner 65534, mode 0400, in a
directory of mode 0500, before the container starts. The key is never in an
argument, in the container's environment or in a log. The workspace shares
no volume with the model container. Because the model container drops every
capability, even root inside it cannot read the directory.

### 3. The run token

The driver generates the token per agent launch: 32 bytes from
`crypto/rand`, in hex. It is copied to the model container as
`/forja/token` and given to the workspace as `ANTHROPIC_API_KEY`. The proxy
accepts no other `x-api-key`, compared in constant time; the real key itself
is refused. The token dies with the model container on `Close`, which is
what "expira ao fim" means.

The agent workspace's environment is the image's own, the proxy variables of
ADR 0035, and:

```
ANTHROPIC_BASE_URL=http://forja-model:8080
ANTHROPIC_API_KEY=<run token>
NO_PROXY=localhost,127.0.0.1,::1,forja-model   (and no_proxy)
```

### 4. The cap

`FORJA_MODEL_TOKEN_CAP` is a positive integer: total tokens per run, counting
input, both cache fields and output. It is operator configuration, like
`FORJA_EGRESS_ALLOW`. No contract field is added.

The proxy forwards one request at a time. A request waits until the
previous one's usage is counted, so requests sent in parallel cannot all be
admitted while the total is still low.

The proxy checks the cap before it forwards. A request is admitted only when
its `max_tokens` fits what the cap leaves. Once a response brings the total
to the cap or past it, or a request's `max_tokens` does not fit, the proxy
writes one cap line. From then on it refuses every request with ADR 0029's
429 and body, without calling upstream. The refusal is sticky. Output cannot
overshoot the cap. Input can overshoot it by one request's input at most.
The cap must therefore exceed the agent's `max_tokens`, which is 128 000 for
Claude Code. The status stays 429; 400 remains on ADR 0029's checklist.

### 5. The upstream

`FORJA_MODEL_UPSTREAM` defaults to `https://api.anthropic.com`. It must be an
`http` or `https` URL with a host and no path, query, fragment or userinfo.
Plain `http` exists only so tests can point the proxy at a fake.

### 6. Two routes and nothing else

- `HEAD /api/hello` is answered 200 locally, without a token, as Claude Code
  sends it without one.
- `POST /v1/messages` is forwarded with its query string.

Every other method or path gets 404 without reaching the upstream:
`count_tokens`, `batches`, `GET /v1/messages`, `/v1/complete`, encoded
variants. The workspace is untrusted, and an unmetered route would bypass
the cap.

The model API acts on the body and on `anthropic-beta`, and some of what it
can do reaches a host the request names. The workspace could use that to get
past its egress, so the proxy reads every body whole, up to the API's own
32 MiB, and answers 400 without calling upstream when the body:

- is not one JSON object;
- has no positive integer `max_tokens`;
- has `mcp_servers`;
- has a tool whose `type` is not `custom` (a server tool);
- has a content block, in `system`, a message, a tool result or a content
  document, whose `source` is not `base64`, `text` or `content` (a URL or a
  file id).

The body forwarded is the one the proxy re-encoded from what it checked. A
key that appears twice therefore reaches the upstream once, as the proxy
read it. Of `anthropic-beta`, only the seven values Claude Code sends in
`--bare` are forwarded, as recorded in the contract; every other value is
dropped.

The proxy drops `Authorization`, sets `x-api-key` to the real key, and sets
`Accept-Encoding: identity`. Claude Code sends `gzip, deflate, br, zstd`;
forwarded as is, the upstream could compress the stream and the meter would
read nothing. A response that arrives encoded anyway is answered 502 and not
forwarded.

### 7. Metering

The proxy reads the body as it streams to the client:

- `text/event-stream`: `message_start.message.usage` gives the input, both
  cache fields and a first output count; `message_delta.usage.output_tokens`
  replaces the output count, since it is the message's running total.
- `application/json` (`stream: false`): the top-level `usage`, which has the
  shape `message_start` carries.

It counts what it saw, including a stream cut short or a client that left.
A successful response that never said its output is charged the request's
`max_tokens`, because a stream closed before `message_delta` was still
generated upstream. One that never said its input is charged the body's
size in bytes. A response that is not a success is charged what it said,
which is nothing.

It writes one JSON line per forwarded response, `{event: usage, run, at,
status, usage: {input, cache_creation, cache_read, output}}`, and one cap
line. No body, prompt, header, key or token is written. If a line cannot be
written, the proxy stops, as the gateway does: an unrecorded response is a
cap bypass.

The control plane reads the log with `Container.Model`, which refuses the
log of a proxy that is no longer running, a line of another run, or a line it
does not understand. The agent's own JSON on stdout is never read.

### 8. The agent in the workspace

- **Binary.** `FORJA_AGENT_BINARY` is a linux `claude` for the daemon's
  architecture, copied to `/usr/local/bin/claude` as root 0755. A key without
  it is a startup error; the launch fails closed without it.
- **Invocation.**

  ```
  GIT_AUTHOR_NAME='Forja agent' GIT_AUTHOR_EMAIL=agent@forja.invalid \
  GIT_COMMITTER_NAME='Forja agent' GIT_COMMITTER_EMAIL=agent@forja.invalid \
  GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=safe.directory GIT_CONFIG_VALUE_0=/workspace \
  CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 \
  claude -p --bare --output-format json --no-session-persistence \
    --permission-mode dontAsk --permission-prompts none \
    --tools Read,Edit,Write,Bash,Glob,Grep \
    --allowedTools Read,Edit,Write,Bash,Glob,Grep \
    < /forja/prompt.md
  ```

  It is ADR 0029's form with three changes:
  - no `--max-budget-usd`, because the cap is in tokens and a USD figure
    needs the price table #14 owns;
  - `--allowedTools` added, because in `dontAsk` mode a tool only listed in
    `--tools` is denied (measured below);
  - no prompt argument and no `--append-system-prompt`: the prompt goes in on
    stdin.
- **Prompt.** The control plane renders one file: the issue title and body,
  every `agent_context` file read from the base checkout, with its path, in
  order; the contract's gate commands; and the instruction to commit without
  pushing. Each file sits in a fence longer than any run of backticks inside
  it. It is written to `/forja/prompt.md`, outside `/workspace`, so it is
  never committed. No issue text reaches the command line, so nothing
  untrusted is quoted for a shell. Neither argv nor `--append-system-prompt`
  could carry it: the text files of this repository's `agent_context` alone
  are about 132 KB, and Linux limits one argument to 128 KiB.
- **Git.** The identity is fixed and names no person. The tree is copied
  into the workspace owned by the control plane's uid, not by the agent's
  root, so git would refuse the repository as of dubious ownership;
  `safe.directory` comes from the command scope, which git honours.
- **Run.** `agent.Runner` writes the prompt and runs the command under
  `limits.wall_clock`. A goroutine it owns follows the proxy's log and
  cancels the agent when the cap line arrives; it stops when the agent
  returns. Then the runner halts the workspace container, so nothing in it
  reaches a proxy unrecorded, reads the usage and the egress, and closes the
  run on every path. A deadline is a result (`TimedOut`); a cancellation is
  an error.

### forja.implement and forja.status

After the run is recorded running, `forja.implement` clones `base_branch`,
reads the contract and the `agent_context` from that checkout and the issue
from GitHub, and runs the agent. The terminal record carries the tokens and
the egress on every outcome. It is failed with reason `token_cap` when
capped; failed with no reason on a non-zero exit or a deadline; failed on an
infrastructure error, which is also returned. The claim is released on every
path. It is succeeded only when the agent exited 0, uncapped, and the release
worked.

`forja.status` gains `tokens_in` (input plus both cache fields) and
`tokens_out`, always on the wire and 0 when the run called no model, and
`failure_reason`, present only when set.

## Measurements

Environment: local Docker on colima 29.5.2, linux/arm64; Claude Code
2.1.316. Everything below ran against fakes; no real key was used.

The measurer's runs for this slice, before any code:

| Question | Result |
|---|---|
| ADR 0029's flags (`dontAsk`, `--tools` only) | 3 of 3 runs: Bash and Edit denied in `permission_denials`, exit 0, nothing written |
| Same plus `--allowedTools Read,Edit,Write,Bash,Glob,Grep` | 3 of 3 on macOS, 4 of 4 in `golang:1.25` as root: both tools ran |
| `--permission-mode bypassPermissions` as root | refused, exit 1 |
| Prompt on stdin, 143 360 B and 1 048 576 B | delivered whole, both markers present (3 of 3, 1 of 1) |
| `NO_PROXY` without `forja-model` | 3 of 3: the gateway refused `forja-model:8080`, the agent exited 1 with a 403 |
| `NO_PROXY` with `forja-model` | 4 of 4: every POST reached the model directly, 0 denials |
| Without `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1` | 2 denials of `api.anthropic.com:443` per run; with it, 0 |
| A container on the internal network plus `bridge` reaches a bridge address | 3 of 3; internal only: refused |

The slice's own Docker tests, real Engine:

- From a `LaunchAgent` workspace, `curl` to `$ANTHROPIC_BASE_URL/v1/messages`
  gets the fake upstream's stream; the fake answers only for the real key.
  `https://example.com` from the same workspace is refused and is the one
  denial in `Egress()`.
- Two launches get different tokens of 64 hex characters, neither the key.
  The workspace has no key, no `/forja/key`, no file holding the key; the
  model container's `Config.Env` and `Cmd` hold neither secret; the secret
  files are `65534 400`.
- After `Close`, the workspace, the model proxy, the gateway and the network
  are NotFound. A launch with a bad image, no agent binary, no key or a proxy
  that cannot start leaves nothing labelled with its run. A plain `Launch`
  creates no model container.
- With a cap of 1, the first `curl` gets 200, `AwaitCap` returns, the second
  gets the 429.
- `FORJA_AGENT_TEST=1` with the real linux `claude`, against a fake scripted
  with one Bash turn that commits and an end_turn: exit 0, the agent's commit
  on top of the base in `git log`, tokens equal to the sum the fake served
  (201 input, 20 cache creation, 40 cache read, 18 output), 0 denials.
  Without `--allowedTools`, or without `safe.directory`, the same test finds
  no commit.

## Consequences

- Each agent run costs a second small container and a second forjad copy,
  and a second endpoint on `bridge`.
- The proxy listens on every interface of its container, so other containers
  on `bridge` can reach it. They need the run token, which only that run's
  workspace holds.
- Tokens are visible only once the run is terminal; partial tokens while it
  runs belong to #14.
- `agent.Runner` closes the workspace. Whether the gates of #13 reuse it,
  and whether the proxy is revoked before they run, is #13's decision.
- A run whose base branch is not the default branch takes its lease from the
  default branch's contract and its wall clock from the base branch's. When
  the second is longer, the lease can expire mid-run. This predates the
  slice and is not fixed here.

## What only the human can do

- [ ] Ratify the model proxy as a per-run sibling with the key as a file, or
      choose the gateway's allowlist instead. On ratification, change this
      ADR's status to `accepted`.
- [ ] Ratify the cap as operator configuration (`FORJA_MODEL_TOKEN_CAP`), or
      ask for a contract field.
- [ ] Ratify `--allowedTools` next to `--tools`: it lets the agent run any
      Bash command inside the workspace without asking, which is the point
      of the workspace's isolation.
- [ ] Decide the items still open on ADR 0029's checklist: 429 or 400, the
      pinned `--model` and `--effort`, and the spend to fill its comparison
      table with the real key.
- [ ] Measure the agent launch on the GitHub Actions runner; it was measured
      only on colima, and linux/amd64 only under emulation.

## References

- `docs/PRD.pdf`, "Segurança e isolamento" and step 4 of `forja.implement`.
- [ADR 0029](0029-workspace-agent.md), [ADR 0035](0035-egress-gateway.md).
- [`docs/contracts/claude-code-headless/`](../contracts/claude-code-headless/README.md).
- `CLAUDE.md`, rules 2, 3, 4 and 6.
