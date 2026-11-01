# Streaming SSE e o bloco `usage`

Fonte: gravação `bare-200-1`. O gravador respondeu ao único
`POST /v1/messages?beta=true` com `200`, `content-type: text/event-stream;
charset=utf-8` e seis eventos SSE escritos à mão. O agente aceitou, imprimiu
o texto como resultado e saiu com **exit 0** em 0,87 s. Não houve segundo
`POST` (nem `count_tokens`, nem geração de título) depois do `message_stop`.

## A resposta mínima que o agente aceitou

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

Cabeçalhos da resposta: `Content-Type: text/event-stream; charset=utf-8`,
`Cache-Control: no-cache`, `Connection: close`, sem compressão.

## Onde o proxy lê o consumo

| Evento | Campo | Valor enviado | Valor que o agente reportou |
|---|---|---|---|
| `message_start` | `message.usage.input_tokens` | 1234 | `usage.input_tokens: 1234` |
| `message_start` | `message.usage.cache_creation_input_tokens` / `cache_read_input_tokens` | 0 / 0 | 0 / 0 |
| `message_delta` | `usage.output_tokens` | 56 | `usage.output_tokens: 56` |

O proxy mede **input + output** por run somando, a cada resposta que
repassa, `message_start.message.usage.input_tokens` (mais os dois campos de
cache, se forem cobrados) e `message_delta.usage.output_tokens`. É a soma
dessas duas leituras, no proxy, que compara com o teto; o que o agente
imprime em `stdout` é derivado do mesmo stream e **não** serve de evidência —
o workspace controla o `stdout`.

## O JSON que `--output-format json` devolve

`stdout` de `bare-200-1`, uma linha, reformatada:

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

`total_cost_usd` é calculado **no cliente** a partir da tabela de preços
embutida (`costBasis: "list"`): 1234 × 4 / 10⁶ + 56 × 20 / 10⁶ = 0,006056. Em
caso de erro de API o mesmo JSON sai com `is_error: true`,
`terminal_reason: "api_error"` e `api_error_status` (ver
[erros-e-retentativas.md](erros-e-retentativas.md)).

Campos úteis ao plano de controle, lidos como **informação** e não como
evidência: `is_error`, `terminal_reason`, `api_error_status`, `num_turns`,
`duration_ms`, `permission_denials`. O código de saída do processo foi 0 no
sucesso e 1 nos erros de API medidos.
