# What Claude Code headless does with 400, 401 and 429

Source: recordings `bare-1`, `bare-401-2`, `bare-401-3` (401), `bare-429-1`,
`bare-429-long-1` (429) and `bare-400-1`, `bare-400-raw-1`,
`bare-default-tools-400-1`, `bare-restricted-400-1` (400). Version `2.1.316`,
flags in [README.md](README.md). The times are the arrival instant of each
`POST` at the listener, relative to the `HEAD /api/hello` of the same process.

Error body returned by the recorder, in the API's format:

```
{"type": "error", "error": {"type": "<authentication_error|invalid_request_error|rate_limit_error>", "message": "…"}}
```

For 429 the recorder added `retry-after: 1`.

## 401 — retried with exponential backoff

Three recordings, 9 `POST` each, process still alive when the recorder killed
it at 120 s (the recorder had `timeout_s = 120`):

| Attempt | `bare-1` | `bare-401-2` | `bare-401-3` |
|---|---|---|---|
| 1 | 0,27 s | 0,23 s | 0,37 s |
| 2 | 0,79 s | 0,77 s | 0,92 s |
| 3 | 1,82 s | 1,79 s | 1,94 s |
| 4 | 4,24 s | 4,00 s | 4,15 s |
| 5 | 8,74 s | 8,63 s | 8,22 s |
| 6 | 16,99 s | 17,21 s | 16,87 s |
| 7 | 33,47 s | 34,93 s | 33,34 s |
| 8 | 68,26 s | 72,10 s | 72,98 s |
| 9 | 104,65 s | 109,21 s | 105,31 s |
| exit | cut at 120 s | cut at 120 s | cut at 120 s |

`stdout` and `stderr` empty in all three (the result JSON only comes out at
the end). The interval doubles up to ~32–36 s and there is ±5 s variation
between recordings (jitter).

## 429 — retried 11 times, gives up at 180 s

| Attempt | `bare-429-1` (cut at 120 s) | `bare-429-long-1` (`timeout_s = 560`) |
|---|---|---|
| 1 | 0,38 s | 0,23 s |
| 2 | 1,38 s | 1,24 s |
| 3 | 2,62 s | 2,29 s |
| 4 | 4,93 s | 4,49 s |
| 5 | 9,58 s | 8,60 s |
| 6 | 17,86 s | 18,22 s |
| 7 | 34,63 s | 34,29 s |
| 8 | 72,02 s | 72,02 s |
| 9 | 105,21 s | 108,27 s |
| 10 | — | 140,34 s |
| 11 | — | 180,04 s |
| exit | cut | **1**, on its own, 180,61 s after the start |

`retry-after: 1` did **not** shorten the backoff: the cadence is the same as
the 401's. Result on `stdout` of the complete sample (usage fields omitted,
all zero):

```
{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error",
 "api_error_status":429,"num_turns":1,"duration_ms":179965,
 "result":"API Error: Request rejected (429) · forja: run token cap reached", …}
```

The proxy's message reaches `result` as is. **One complete sample**: the
number of attempts (11) and the ceiling (180 s) need two more recordings with
`timeout_s ≥ 200` to become a number.

## 400 — not retried

Four recordings with `--bare`: 3 simultaneous `POST` (the variants described
in [messages-request.md](messages-request.md)) and exit 1 right after.

| Recording | `POST`s | Instants | exit | Total process time |
|---|---|---|---|---|
| `bare-400-1` | 3 | 0,40 / 0,40 / 0,40 s | 1 | 1,29 s |
| `bare-400-raw-1` | 3 | 0,34 / 0,34 / 0,35 s | 1 | 1,23 s |
| `bare-default-tools-400-1` | 3 | 0,23 / 0,24 / 0,24 s | 1 | 0,85 s |
| `bare-restricted-400-1` | 3 | 0,47 / 0,47 / 0,47 s | 1 | 1,10 s |
| `nobare-400-1` | 6 | 2,52 … 2,54 s | 1 | 3,80 s |

`stdout` (from `bare-400-1`, usage fields omitted, all zero):

```
{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error",
 "api_error_status":400,"num_turns":1,"duration_ms":324,
 "result":"API Error: 400 forja recorder: request recorded", …}
```

## Consequence for the proxy's token cap

| Proxy response on exceeding the cap | Measured effect on the agent |
|---|---|
| 429 | keeps trying for 180 s (11 requests, 1 sample) and only then exits with exit 1; each attempt is a complete request the proxy has to refuse again |
| 400 | exits with exit 1 in under 1,3 s (4 of 4) |
| 401 | keeps trying beyond 120 s (3 of 3); end not measured |

In every case the process ends with `is_error: true` and `api_error_status`
equal to the proxy's status, and `result` carries the error body's message.
The exit code is 1 in both cases where the process ended on its own (400 and
429).
