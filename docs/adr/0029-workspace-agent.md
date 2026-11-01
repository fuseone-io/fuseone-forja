# ADR 0029 — Agent that runs in the workspace (provisional default)

- **Status:** proposed — default taken by the team; **human ratification pending**
- **Date:** 2026-11-01
- **Issue:** Refs #2 (stays open until ratification)
- **Measured base:** `origin/main` at `ce95147dea46427850e944c22186195fb9e23c89`
- **Recordings:** [`docs/contracts/claude-code-headless/`](../contracts/claude-code-headless/README.md)

## Context

The PRD places in phase 0 a spike to choose the agent that runs inside the
workspace: **Claude Code headless** or **a custom loop over the model API**.
The choice decides three things that the rest of Forja inherits: the cost per
run (tokens and minutes), the control over what the agent can execute and
reach, and the shape of the model proxy (`internal/proxy/model`) — protocol,
streaming, headers and the token-cap mechanism.

Issue #2 asks for both options running the same issue, in a container with no
credential, three times each, with a table of tokens, minutes, green gates and
interventions. That requires the model proxy and the pilot repository, which
do not exist yet (ADR 0027 records the pilot as a provisional default). The
human asked the team to proceed without interruptions. This ADR, therefore,
records the **default** taken, measures in read-only mode what the proxy needs
to support — recording the real protocol Claude Code speaks — and leaves the
comparison table with the cells that could not be measured marked
`not measured`, never estimated.

## Decision (default taken)

### Agent: Claude Code headless

`claude -p` in `--bare` mode, `--output-format json` output, tools restricted
to the workspace, no session persistence. Invocation form that the driver
(`internal/agent`) uses, with the flags that `claude --help` of version
`2.1.316` lists:

```
ANTHROPIC_BASE_URL=http://<run-model-proxy> \
ANTHROPIC_API_KEY=<opaque run token> \
claude -p --bare \
  --output-format json \
  --no-session-persistence \
  --permission-mode dontAsk --permission-prompts none \
  --tools Read,Edit,Write,Bash,Glob,Grep \
  --max-budget-usd <cap in USD, redundant with the proxy's> \
  --append-system-prompt "<agent_context rendered by the control plane>" \
  "<issue title and body, acceptance criteria, contract gates>"
```

Why `--bare`: measured in
[messages-request.md](../contracts/claude-code-headless/messages-request.md),
without `--bare` the agent loaded the host's configuration (12 and then 51
tools, 27 of them from MCP servers configured outside the workspace, body of
179 267 bytes) and warned that `ANTHROPIC_API_KEY` takes precedence over the
claude.ai login. With `--bare`, the system prompt has 3 blocks (billing
header, one identity sentence and `CWD`/`Date`) and the body 4 470 bytes: no
hook, no `CLAUDE.md` discovered, no keychain. Inside the workspace, "project
configuration" would be the agent's tree configuring the agent itself —
`--bare` closes that path.

Why `--permission-prompts none` and `--permission-mode dontAsk`: there is no
human to answer; what would require approval is denied and appears in
`permission_denials` in the final JSON.

### Alternative rejected for phase 0: custom loop over the API

A custom loop speaks the **same** protocol with the proxy — `POST /v1/messages`
with `stream: true`, `tools`, `tool_use`/`tool_result` — and therefore does
not change the proxy's shape. What it changes is who rebuilds: tool execution
(shell, reading and editing files with the diff applied), the permission
policy, context management (compaction, `thinking` cleanup), retries and the
usage report. Claude Code already delivers all of that and was measured
talking to a local listener with no adaptation. Rejected for the cost of
rebuilding tool control; **not measured** because it was not built. It comes
back to the table if **one** of these conditions occurs:

1. the comparison table, once measured, shows that Claude Code spends more
   than 2× the tokens of a minimal loop on the same issue; or
2. a Claude Code version changes the protocol in a way that the proxy can no
   longer measure `usage` (for example, stops sending
   `message_delta.usage`).

## What the model proxy needs to support

Everything below was observed in 11 recordings, 66 `POST`, in this session
([README.md](../contracts/claude-code-headless/README.md)). Nothing was assumed
from documentation.

| Item | Observed | What the proxy does |
|---|---|---|
| `ANTHROPIC_BASE_URL` | honoured in 11 of 11 recordings | It is the proxy's address on the run's `--internal` network (ADR 0026) |
| `HEAD /api/hello` | 1 per process, before the first message, without `x-api-key` | Answers (any status served: 200, 400, 401 and 429 were tested) |
| `POST /v1/messages?beta=true` | 66 of 66 calls to the model; `count_tokens` 0 of 66 | Forwards to the upstream with the real key; preserves the query string |
| `x-api-key` | equal to the run's `ANTHROPIC_API_KEY`, byte for byte, in 66 of 66 | Opaque token per run: the proxy resolves it to the real key and validates it against the active run; the real key never enters the workspace |
| `anthropic-version: 2023-06-01` | 66 of 66 | Forwards |
| `anthropic-beta` | 7 items in `--bare`; varies per flag and per retry | Forwards without filtering |
| `stream: true` | 66 of 66 | Forwards SSE in `text/event-stream`, event by event; does not buffer |
| `role: "system"` inside `messages` | 66 of 66 (`output_config.effort` message) | Accepts and forwards; the proxy does not validate the body |
| `x-stainless-timeout: 600` | 66 of 66 | Does not cut before 600 s; the run's `wall_clock` is what ends it |
| `usage` | `message_start.message.usage.input_tokens` and `message_delta.usage.output_tokens`, echoed to `stdout` exactly as sent (1234 / 56) | It is where the proxy measures |
| `metadata.user_id` | JSON with `device_id`, empty `account_uuid` and `session_id` | Not a credential; can be replaced by the run id for correlation |

Full list of headers and the body's shape:
[messages-request.md](../contracts/claude-code-headless/messages-request.md).
Minimal accepted SSE response and output JSON:
[streaming-and-usage.md](../contracts/claude-code-headless/streaming-and-usage.md).

## Token cap

### Mechanism

1. The control plane issues **one opaque token per run** and hands it to the
   workspace in `ANTHROPIC_API_KEY`. The proxy maps token → run → real key.
2. On each response it forwards, the proxy adds to the run
   `message_start.message.usage.input_tokens` +
   `cache_creation_input_tokens` + `cache_read_input_tokens` and
   `message_delta.usage.output_tokens`. The total is persisted in the
   `runstore` as `domain.Cost` (integers).
3. When the total exceeds the contract's `limits`, the proxy **refuses with
   429** and body `{"type":"error","error":{"type":"rate_limit_error","message":
   "forja: run token cap reached"}}`, without calling the upstream.
4. On refusing, the proxy notifies the control plane, which **revokes the
   token** and terminates the workspace. The cap is enforced by the plane,
   not by the agent's cooperation.

The command line's `--max-budget-usd` is agent-side redundancy, not evidence:
the workspace controls the process that computes it.

### What the measurement showed about the 429

[errors-and-retries.md](../contracts/claude-code-headless/errors-and-retries.md):
Claude Code **retries** the 429 with exponential backoff — 11 requests in
180,0 s before exiting with exit 1 (1 complete sample; another sample cut at
120 s with 9 requests) — and `retry-after: 1` did not shorten the backoff. A
400 is **not** retried: exit 1 in under 1,3 s in 4 of 4 recordings.

Consequences for step 4 above: without revocation by the plane, the workspace
would keep hammering the proxy for up to 180 s after the cap (cheap — no call
to the upstream — but wall-clock time spent). With revocation and workspace
termination, the 429 becomes only the record of the reason. The alternative —
answering 400 so the agent stops on its own in ~1 s — is left for the human
to decide (see checklist); 429 is the default because it is the status that
describes the fact and because the final JSON's `api_error_status` preserves
it.

## What the agent reads and what it executes

### Reads (input the control plane assembles)

| Source | How it reaches the agent | Note |
|---|---|---|
| Repository tree on the run's branch | checkout done by the control plane in the workspace; the agent uses `Read` and `Bash` | It is the only thing the agent reads on its own |
| The contract's `agent_context` (`.forja.yaml` of the base commit; today `CLAUDE.md`, `CONTRIBUTING.md`, `docs/`) | rendered by the plane in `--append-system-prompt`, or a list of paths in the prompt for the agent to read | Measured: in `--bare` there is no `CLAUDE.md` discovery; what is not passed explicitly does not arrive |
| Issue title, body and acceptance criteria | in the prompt, by the control plane | The plane does not log the body (personal data); it references the number |
| The contract's gate commands (`make build`, `make vet`, `make lint`, `make test`, `make race`) | in the prompt, as "what will be checked" | The agent may run them with `Bash` to iterate; the gate of record is the plane's, outside the workspace |

Does not read: `.forja.yaml` as authority (the contract comes from the base
commit, rule 2 of `CLAUDE.md`); credentials (they do not exist in the
workspace, rule 4); the evidence (written outside, rule 3).

### Executes (tools in the `tools` array)

| Tool | Requested | Sent to the model (measured) | Reach |
|---|---|---|---|
| `Bash` | yes | yes (schema of 1 616 bytes) | Any command inside the workspace; network only up to the proxies (ADR 0026) |
| `Edit` | yes | yes | In-place editing of workspace files |
| `Read` | yes | yes | Reading of workspace files |
| `Write`, `Glob`, `Grep` | yes | **did not appear** in the array in 9 of 9 recordings | Observed; cause not measured (the agent can cover them with `Bash`) |

Cannot: push (no git credential), write `forja/gates` or `forja/qa` (they
stay outside the workspace), reach any host beyond the proxies, read the
model's real key (it only sees the opaque token).

## Comparison table requested by the issue

No cell was measured: both options require the model proxy (to reach a model
without a credential in the workspace) and the pilot repository (for the same
issue and the same gates). The custom loop was not built.

| Option | Tokens (input+output) | Minutes | Green gates | Interventions | Runs |
|---|---|---|---|---|---|
| Claude Code headless | not measured | not measured | not measured | not measured | 0 of 3 |
| Custom loop over the API | not measured | not measured | not measured | not measured | 0 of 3 |

What the session measured instead — the protocol, the retries and the `usage`
path — is in the recordings. Once the proxy exists, the table is filled by
running `recorder.py` with status 200 swapped for real forwarding, or the
driver directly, three times per option, in the container of ADR 0026.

## Consequences

- `internal/proxy/model` is an HTTP reverse proxy for the Messages API with
  three obligations: swap `x-api-key` for the real key, forward SSE without
  buffering and read `usage` in two events. It needs neither to validate the
  body nor to know tools.
- `internal/agent` is a process driver: it assembles the prompt, sets two
  environment variables, runs `claude -p --bare …` with the `wall_clock` as
  the `context` deadline and reads **one** JSON from `stdout` as information
  (never as evidence).
- The token cap lives in the proxy and in the `runstore`; the agent only
  perceives it as a 429. The plane terminates the workspace on refusal —
  without that, up to 180 s of retries (measured).
- Every new Claude Code version is a potential protocol change. The
  `recorder.py` re-run against the new version is the contract's regression
  test; the `anthropic-beta` list already varied per flag in this version.
- The real cost per run remains unknown until the table is filled; the proxy
  slice must not be sized by estimate.

## What only the human can do

- [ ] Ratify Claude Code headless (`--bare`) as the phase-0 agent, or choose
      the custom loop. On ratification, change the status to `accepted`.
- [ ] Decide the cap status: 429 (default, describes the fact, up to 180 s of
      retries without revocation) or 400 (the agent stops in ~1 s, status
      semantically wrong).
- [ ] Authorise the spend to fill the comparison table: 3 runs per option on
      the same pilot issue, via proxy, in a container — real tokens.
- [ ] Decide whether the custom loop is built as a measurement alternative
      (to fill its row) or whether the row stays `not measured` until one of
      the revisit conditions.
- [ ] Confirm the model and the `effort` the driver pins (`--model`,
      `--effort`): the measurement used the version's default,
      `claude-opus-5-5` with `effort: high`.

## Commands that produced the numbers

All run in this session, on 2026-11-01, on the maintainer's laptop (macOS,
arm64), with the recorder and the agent on the same machine.

```
/Users/phb/.local/bin/claude --version
/Users/phb/.local/bin/claude --help
/usr/bin/git rev-parse origin/main
docker info --format '{{.ServerVersion}} {{.OperatingSystem}}'
python3 -I recorder.py <dir> bare-1 <ws> 401 -- -p --bare --output-format json --permission-mode dontAsk --permission-prompts none --tools Read,Edit,Write,Bash,Glob,Grep --max-budget-usd 1 "List the files in this directory and say what README.md contains."
python3 -I recorder.py <dir> bare-401-2 <ws> 401 120 -- (same flags)
python3 -I recorder.py <dir> bare-401-3 <ws> 401 120 -- (same flags)
python3 -I recorder.py <dir> bare-429-1 <ws> 429 -- (same flags)
python3 -I recorder.py <dir> bare-429-long-1 <ws> 429 560 -- (same flags)
python3 -I recorder.py <dir> bare-400-1 <ws> 400 -- (same flags)
python3 -I recorder.py <dir> bare-400-raw-1 <ws> 400 120 -- (same flags)
python3 -I recorder.py <dir> bare-default-tools-400-1 <ws> 400 -- (same flags without --tools)
python3 -I recorder.py <dir> bare-restricted-400-1 <ws> 400 -- (same flags plus --restricted)
python3 -I recorder.py <dir> bare-200-1 <ws> 200 120 -- (same flags)
python3 -I recorder.py <dir> nobare-400-1 <ws> 400 -- -p --output-format json --permission-mode dontAsk --permission-prompts none --max-budget-usd 1 "List the files in this directory and say what README.md contains."
```

`<dir>` and `<ws>` are the session's temporary directories; the raw JSON
recordings (with the full body, including the workspace path) stayed outside
the repository. `recorder.py` is in
`docs/contracts/claude-code-headless/`.

## References

- `docs/PRD.pdf` — Forja: execution runtime of the software factory.
- `CLAUDE.md` — rules 2 (contract from the base commit), 3 (evidence by the
  control plane), 4 (no credential in the workspace).
- ADR 0026 — workspace isolation (`--internal` network per run and proxies).
- ADR 0027 — pilot repository (provisional default) and `agent_context` of
  `.forja.yaml`.
- Issue #2.
