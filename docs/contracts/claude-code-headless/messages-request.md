# The `POST /v1/messages` request as Claude Code headless sends it

Source: recording `bare-400-raw-1` (raw body kept) cross-checked with the
other 10 recordings of [README.md](README.md). Version `2.1.316`. Secret
values redacted; session identifiers replaced with `<…>`.

## Endpoints seen

In 11 recordings and 77 requests (11 `HEAD` + 66 `POST`), only two paths:

| Order | Method | Path | When | Relevant headers |
|---|---|---|---|---|
| 1 | `HEAD` | `/api/hello` | once, at startup, before the first message | `user-agent: Bun/1.4.4`; **no** `x-api-key`, no `anthropic-version` |
| 2… | `POST` | `/v1/messages?beta=true` | every call to the model (66 of 66) | table below |

The `HEAD /api/hello` is a connectivity probe: the recorder answered
401/400/429/200 to it and the agent proceeded to the `POST` in every case (11
of 11). The proxy needs to **answer** it; the status did not matter in the
recordings.

`POST /v1/messages/count_tokens`: 0 occurrences in 66 `POST`. See README.

## Headers of the `POST /v1/messages`

As they reached the listener (client order), recording `bare-1`, first
request:

```
accept: application/json
content-type: application/json
user-agent: claude-cli/2.1.316 (external, sdk-cli)
x-claude-code-session-id: <session uuid>
x-stainless-arch: arm64
x-stainless-lang: js
x-stainless-os: MacOS
x-stainless-package-version: 0.128.0
x-stainless-retry-count: 0
x-stainless-runtime: node
x-stainless-runtime-version: v26.12.1
x-stainless-timeout: 600
anthropic-beta: claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27,per-turn-control-2026-07-01,advisor-tool-2026-03-01,thinking-token-count-2026-05-13,effort-2025-11-24
anthropic-dangerous-direct-browser-access: true
anthropic-version: 2023-06-01
x-api-key: <redacted — 22 bytes, equal to the run's ANTHROPIC_API_KEY>
x-app: cli
connection: keep-alive
host: 127.0.0.1:<port>
accept-encoding: gzip, deflate, br, zstd
content-length: 4470
```

What the proxy must do with each one:

| Header | Observed | For the proxy |
|---|---|---|
| `x-api-key` | equal to the value of `ANTHROPIC_API_KEY`, byte for byte, in 66 of 66 `POST`; never in `authorization` | It is the **run's opaque token**. The proxy swaps it for the real key and never forwards it |
| `anthropic-version` | `2023-06-01` in 66 of 66 | Forward as is |
| `anthropic-beta` | list with 7 items in `--bare` (above); 10 items without `--bare` (adds `mid-conversation-tool-changes-2026-07-01`, `inline-tools-2026-09-15`, `advanced-tool-use-2025-11-20`); on the post-400 retry `thinking-token-count-2026-05-13` drops out | Forward as is; do not filter — the list varies per flag and per retry |
| `x-stainless-timeout` | `600` (seconds) | The proxy cannot cut the connection before that; the run's `wall_clock` is what cuts |
| `x-stainless-retry-count` | always `0`, even on the 11th attempt after 429 | The retries belong to Claude Code's loop, not to the SDK; the proxy cannot tell them apart by this header |
| `x-claude-code-session-id` | one UUID per process | Not a credential; useful to correlate the run in the proxy's logs |
| `accept-encoding` | `gzip, deflate, br, zstd` | An uncompressed response was accepted (`bare-200-1`) |
| `anthropic-dangerous-direct-browser-access`, `x-app` | `true`, `cli` | Forward |

## Body of the `POST /v1/messages` (`--bare` mode)

4 470 bytes. Top-level keys, in 66 of 66 `POST`:

```
context_management, max_tokens, messages, metadata, model, output_config, stream, system, thinking, tools
```

Observed values:

| Field | Value in `bare-400-raw-1` |
|---|---|
| `model` | `claude-opus-5-5` (without `--model`) |
| `stream` | `true` (66 of 66) |
| `max_tokens` | `128000` |
| `thinking` | `{"type": "adaptive", "display": "omitted"}`; absent in the 3rd post-400 variant |
| `output_config` | `{"effort": "high"}`; with `--restricted` it came as `"medium"` (1 recording) |
| `context_management` | `{"edits": [{"type": "clear_thinking_20251015", "keep": "all"}]}` |
| `metadata` | `{"user_id": "<JSON with device_id, empty account_uuid and session_id>"}` — device identifier, not a credential |

### `system` — 3 blocks

| # | Size | Content | `cache_control` |
|---|---|---|---|
| 0 | 74 chars | `x-anthropic-billing-header: cc_version=2.1.316.f93; cc_entrypoint=sdk-cli;` | — |
| 1 | 62 chars | `You are a Claude agent, built on Anthropic's Claude Agent SDK.` | `{"type": "ephemeral"}` |
| 2 | 160 chars | `CWD: <workspace path>\nDate: 2026-11-01` | `{"type": "ephemeral"}` |

In `--bare` the system prompt carries no `CLAUDE.md`, memory or host
instructions: only what the command line gives (`--system-prompt`,
`--append-system-prompt`, `--add-dir`). That is why the contract's
`agent_context` has to be delivered explicitly (see ADR).

### `messages` — 2 entries

```
[{"role": "user", "content": [{"type": "text", "text": "<prompt>", "cache_control": {"type": "ephemeral"}}]},
 {"role": "system", "content": [], "output_config": {"effort": "high"}}]
```

The second entry is the mid-conversation system message that pins the
`effort` per turn; the proxy needs to accept `role: "system"` inside
`messages`.

### `tools` — 3 tools

Requested: `--tools Read,Edit,Write,Bash,Glob,Grep`. Sent:

| Name | Schema bytes | `description` |
|---|---|---|
| `Bash` | 1 616 | `execute shell commands` |
| `Edit` | 666 | `modify file contents in place` |
| `Read` | 1 200 | `read files, images, PDFs, notebooks` |

Same 3 without `--tools` and with `--restricted`. The `Bash` schema has
`command` (required), `timeout`, `description` and
`dangerouslyDisableSandbox`, with `additionalProperties: false`.

## Body without `--bare` (recording `nobare-400-1`)

Two sizes in the same process: 81 155 bytes with 12 tools, then
179 267 bytes with 51 tools (24 native + 27 `mcp__*` from the MCP servers
configured **on the host**). Native tools seen: `Agent`, `Bash`,
`CronCreate`, `CronDelete`, `CronList`, `DesignSync`, `Edit`, `EnterWorktree`,
`ExitWorktree`, `ListAgents`, `LSP`, `Monitor`, `NotebookEdit`,
`PushNotification`, `Read`, `ReportFindings`, `ScheduleWakeup`, `SendMessage`,
`Skill`, `TaskStop`, `WebFetch`, `WebSearch`, `Workflow`, `Write`.

Consequence for Forja: without `--bare` the agent loads the host's and the
project's configuration (`.claude/`, `.mcp.json`, hooks). Inside the
workspace that would be the agent's tree configuring the agent itself.
`--bare` cuts that path.

## Post-400 error variants

After a 400 the agent fired, at the same instant (Δ ≤ 0,01 s), 3 `POST`:

1. identical to the original;
2. identical to the original;
3. without the `thinking` key and without `thinking-token-count-2026-05-13` in
   `anthropic-beta` (4 450 bytes).

Then it exited with exit 1. In 4 of 4 recordings with 400 and `--bare`;
without `--bare` there were 6 `POST` (3 with 12 tools, 3 with 51).
