# SSE streaming and the `usage` block

Source: recording `bare-200-1`. The recorder answered the single
`POST /v1/messages?beta=true` with `200`, `content-type: text/event-stream;
charset=utf-8` and six hand-written SSE events. The agent accepted, printed
the text as the result and exited with **exit 0** in 0,87 s. There was no
second `POST` (neither `count_tokens` nor title generation) after the
`message_stop`.

## The minimal response the agent accepted

```
event: message_start
data: {"type":"message_start","message":{"id":"msg_forja_recorder","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1234,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"forja-recorder-ok"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":56}}

event: message_stop
data: {"type":"message_stop"}
```

Response headers: `Content-Type: text/event-stream; charset=utf-8`,
`Cache-Control: no-cache`, `Connection: close`, no compression.

## Where the proxy reads consumption

| Event | Field | Value sent | Value the agent reported |
|---|---|---|---|
| `message_start` | `message.usage.input_tokens` | 1234 | `usage.input_tokens: 1234` |
| `message_start` | `message.usage.cache_creation_input_tokens` / `cache_read_input_tokens` | 0 / 0 | 0 / 0 |
| `message_delta` | `usage.output_tokens` | 56 | `usage.output_tokens: 56` |

The proxy measures **input + output** per run by adding, on each response it
forwards, `message_start.message.usage.input_tokens` (plus the two cache
fields, if they are billed) and `message_delta.usage.output_tokens`. It is
the sum of those two readings, in the proxy, that is compared with the cap;
what the agent prints to `stdout` is derived from the same stream and does
**not** serve as evidence — the workspace controls `stdout`.

## The JSON that `--output-format json` returns

`stdout` of `bare-200-1`, one line, reformatted:

```
{"type":"result","subtype":"success","is_error":false,"terminal_reason":"completed",
 "api_error_status":null,"num_turns":1,"stop_reason":"end_turn",
 "result":"forja-recorder-ok",
 "session_id":"<uuid>","uuid":"<uuid>",
 "duration_ms":158,"duration_api_ms":7,"ttft_ms":154,"ttft_stream_ms":154,
 "time_to_request_ms":151,"first_content_frame_ms":154,
 "total_cost_usd":0.006056000000000001,
 "usage":{"input_tokens":1234,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,
          "output_tokens":56,"output_tokens_details":{"thinking_tokens":0},
          "server_tool_use":{"web_search_requests":0,"web_fetch_requests":0},
          "service_tier":"standard",
          "cache_creation":{"ephemeral_1h_input_tokens":0,"ephemeral_5m_input_tokens":0},
          "inference_geo":"","iterations":[],"speed":"standard","fallback_credit":null},
 "modelUsage":{"claude-opus-5-5":{"inputTokens":1234,"outputTokens":56,
          "cacheReadInputTokens":0,"cacheCreationInputTokens":0,"webSearchRequests":0,
          "costUSD":0.006056000000000001,"contextWindow":1000000,"maxOutputTokens":128000,
          "thinkingTokens":0,"cacheCreationEphemeral5mInputTokens":0,
          "cacheCreationEphemeral1hInputTokens":0,"canonicalModel":"claude-opus-5-5",
          "provider":"firstParty","costBasis":"list"}},
 "permission_denials":[],"safety_stops":0,"queued_turn_count":0,"result_index":0,
 "fast_mode_state":"off","fast_mode_disabled_reason":"sdk_opt_in_required",
 "subagent_stats":{"spawned":0, …}}
```

`total_cost_usd` is computed **on the client** from the built-in price table
(`costBasis: "list"`): 1234 × 4 / 10⁶ + 56 × 20 / 10⁶ = 0,006056. On an API
error the same JSON comes out with `is_error: true`,
`terminal_reason: "api_error"` and `api_error_status` (see
[errors-and-retries.md](errors-and-retries.md)).

Fields useful to the control plane, read as **information** and not as
evidence: `is_error`, `terminal_reason`, `api_error_status`, `num_turns`,
`duration_ms`, `permission_denials`. The process exit code was 0 on success
and 1 on the measured API errors.
