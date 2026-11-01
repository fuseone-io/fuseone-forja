# Observed contract: Claude Code headless → model API

Recordings of the HTTP protocol that Claude Code in headless mode (`claude -p`)
speaks with the address in `ANTHROPIC_BASE_URL`. It is what Forja's model
proxy (`internal/proxy/model`) needs to accept, forward and measure. Nothing
here is read from documentation: every line came from a request received by
a local listener in this session.

Documents:

| Document | What it records |
|---|---|
| [messages-request.md](messages-request.md) | Endpoints, method, query string, headers and body shape of `POST /v1/messages` |
| [errors-and-retries.md](errors-and-retries.md) | What the agent does when the proxy answers 400, 401 and 429 — retry chronology and exit code |
| [streaming-and-usage.md](streaming-and-usage.md) | Minimal SSE response the agent accepts, where the `usage` block appears and the JSON that `--output-format json` returns |
| [recorder.py](recorder.py) | The recorder: HTTP listener + execution of `claude -p` under the same process, to reproduce the measurements |

## Method

- **Measured version:** `claude --version` → `2.1.316 (Claude Code)`, installed
  at `/Users/phb/.local/bin/claude`. Observed `User-Agent`:
  `claude-cli/2.1.316 (external, sdk-cli)`.
- **Listener:** `recorder.py` starts a `ThreadingHTTPServer` on
  `127.0.0.1:<random port>`, records method, path, headers (values of
  `x-api-key`, `authorization` and `cookie` redacted to length and equality
  with the run token) and the shape of the JSON body (keys, sizes, tool
  names), and answers with the status requested on the command line.
- **Agent environment:** the recorder clears every `ANTHROPIC_*`,
  `CLAUDECODE` and `CLAUDE_CODE_ENTRYPOINT` variable from the inherited
  environment and sets only `ANTHROPIC_BASE_URL=http://127.0.0.1:<port>` and
  `ANTHROPIC_API_KEY=forja-run-token-<label>` — an opaque token, not a real
  key. The working directory is a disposable workspace with a single
  `README.md` of 107 bytes.
- **Task:** `"List the files in this directory and say what README.md
  contains."` in every recording.
- **Default agent flags:** `-p --bare --output-format json
  --permission-mode dontAsk --permission-prompts none --tools
  Read,Edit,Write,Bash,Glob,Grep --max-budget-usd 1`. The variations are in
  each document.
- **Where it ran:** the maintainer's laptop (macOS, arm64), not a container.
  The listener and the agent were on the same machine; network latency enters
  none of the numbers.

Command that produces a recording (one process, in the foreground):

```
python3 -I recorder.py <out-dir> <label> <workspace> <status> [timeout_s] -- \
  -p --bare --output-format json --permission-mode dontAsk --permission-prompts none \
  --tools Read,Edit,Write,Bash,Glob,Grep --max-budget-usd 1 \
  "List the files in this directory and say what README.md contains."
```

## Recordings of this session

11 recordings, 66 `POST` in total, all on 2026-11-01.

| Label | Status returned | `POST`s | `claude` exit | Total time |
|---|---|---|---|---|
| `bare-1` | 401 | 9 | killed by the recorder at 120 s, still retrying | 120,01 s |
| `bare-401-2` | 401 | 9 | same | 120,01 s |
| `bare-401-3` | 401 | 9 | same | 120,02 s |
| `bare-429-1` | 429 (`retry-after: 1`) | 9 | killed by the recorder at 120 s, still retrying | 120,02 s |
| `bare-429-long-1` | 429 (`retry-after: 1`) | 11 | exit 1 on its own, `api_error_status: 429` | 180,61 s |
| `bare-400-1` | 400 | 3 | exit 1, `api_error_status: 400` | 1,29 s |
| `bare-400-raw-1` | 400 | 3 | exit 1 | 1,23 s |
| `bare-default-tools-400-1` (without `--tools`) | 400 | 3 | exit 1 | 0,85 s |
| `bare-restricted-400-1` (with `--restricted`) | 400 | 3 | exit 1 | 1,10 s |
| `bare-200-1` | 200 fake SSE | 1 | exit 0, `result: "forja-recorder-ok"` | 0,87 s |
| `nobare-400-1` (without `--bare`) | 400 | 6 | exit 1 | 3,80 s |

## Claims checked

| Claim | Result | Evidence |
|---|---|---|
| `ANTHROPIC_BASE_URL` is honoured | **confirmed** | 11 of 11 recordings: every request reached the listener on `127.0.0.1`; no exit to `api.anthropic.com` was possible without the variable and no recording came out empty |
| `ANTHROPIC_API_KEY` is sent as is, in `x-api-key` | **confirmed** | 66 of 66 `POST` with `x-api-key` equal, byte for byte, to the run's `forja-run-token-<label>` token; no `authorization` |
| The agent streams | **confirmed** | 66 of 66 `POST` with `"stream": true` in the body; the fake SSE response of `bare-200-1` was accepted and the agent exited with exit 0 |
| `anthropic-version` and `anthropic-beta` are sent | **confirmed** | `anthropic-version: 2023-06-01` in 66 of 66; `anthropic-beta` in 66 of 66 (list in `messages-request.md`) |
| The agent calls `POST /v1/messages/count_tokens` | **not observed** | 0 of 66 requests; the only paths seen were `HEAD /api/hello` and `POST /v1/messages?beta=true`. Not measured in a long session (compaction) nor with `--autocompact` — every recording ended at the first useful response |
| `--bare` is enough for the agent not to read the host's configuration | **confirmed for what was measured** | With `--bare`, 3 tools and 4 470 bytes of body; without `--bare`, 12 and then 51 tools (27 of them `mcp__*` from the host) and 179 267 bytes. Without `--bare` the `stderr` warned: `claude.ai connectors are disabled because ANTHROPIC_API_KEY or another auth source is set and takes precedence over your claude.ai login` |
| The proxy can deny with 429 and the agent stops | **refuted as "stops immediately"** | 429 is retried: 11 attempts in 180,0 s before exit 1 (1 complete sample; 1 sample of 120 s cut with 9 attempts). 400 is **not** retried: exit 1 in under 1,3 s in 4 of 4 recordings |

## Not measured

- Tokens, minutes and green-gate rate of a real run: they require the proxy
  and the pilot repository. No recording reached a model.
- Behaviour in a container with no credential: local Docker exists
  (`docker info --format '{{.ServerVersion}} {{.OperatingSystem}}'` →
  `29.5.2 Ubuntu 24.04.4 LTS`), but running the agent there without the proxy
  would repeat the same recordings with added latency.
- `count_tokens`, compaction and `--autocompact`: see table above.
- Why `--tools Read,Edit,Write,Bash,Glob,Grep` resulted in only `Bash`,
  `Edit` and `Read` in the `tools` array (3 of 6 requested, in 9 of 9
  recordings with `--bare` and `--tools`; without `--tools`, the same 3):
  observed, cause not measured.
