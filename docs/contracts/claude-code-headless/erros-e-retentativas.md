# O que o Claude Code headless faz com 400, 401 e 429

Fonte: gravações `bare-1`, `bare-401-2`, `bare-401-3` (401), `bare-429-1`,
`bare-429-long-1` (429) e `bare-400-1`, `bare-400-raw-1`,
`bare-default-tools-400-1`, `bare-restricted-400-1` (400). Versão `2.1.316`,
flags em [README.md](README.md). Os tempos são o instante de chegada de cada
`POST` ao listener, relativos ao `HEAD /api/hello` do mesmo processo.

Corpo de erro devolvido pelo gravador, no formato da API:

```
{"type": "error", "error": {"type": "<authentication_error|invalid_request_error|rate_limit_error>", "message": "…"}}
```

Para 429 o gravador acrescentou `retry-after: 1`.

## 401 — retentado com recuo exponencial

Três gravações, 9 `POST` cada, processo ainda vivo quando o gravador o
encerrou aos 120 s (o gravador tinha `timeout_s = 120`):

| Tentativa | `bare-1` | `bare-401-2` | `bare-401-3` |
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
| exit | cortado aos 120 s | cortado aos 120 s | cortado aos 120 s |

`stdout` e `stderr` vazios nas três (o JSON de resultado só sai no fim). O
intervalo dobra até ~32–36 s e há variação de ±5 s entre gravações (jitter).

## 429 — retentado 11 vezes, desiste aos 180 s

| Tentativa | `bare-429-1` (cortado aos 120 s) | `bare-429-long-1` (`timeout_s = 560`) |
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
| exit | cortado | **1**, por conta própria, 180,61 s após o início |

`retry-after: 1` **não** encurtou o recuo: a cadência é a mesma do 401.
Resultado em `stdout` da amostra completa (campos de uso omitidos, todos zero):

```
{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error",
 "api_error_status":429,"num_turns":1,"duration_ms":179965,
 "result":"API Error: Request rejected (429) · forja: run token cap reached", …}
```

A mensagem do proxy chega ao `result` tal qual. **Uma amostra completa**: o
número de tentativas (11) e o teto (180 s) precisam de mais duas gravações
com `timeout_s ≥ 200` para virar um número.

## 400 — não retentado

Quatro gravações com `--bare`: 3 `POST` simultâneos (as variantes descritas
em [requisicao-messages.md](requisicao-messages.md)) e exit 1 em seguida.

| Gravação | `POST`s | Instantes | exit | Tempo total do processo |
|---|---|---|---|---|
| `bare-400-1` | 3 | 0,40 / 0,40 / 0,40 s | 1 | 1,29 s |
| `bare-400-raw-1` | 3 | 0,34 / 0,34 / 0,35 s | 1 | 1,23 s |
| `bare-default-tools-400-1` | 3 | 0,23 / 0,24 / 0,24 s | 1 | 0,85 s |
| `bare-restricted-400-1` | 3 | 0,47 / 0,47 / 0,47 s | 1 | 1,10 s |
| `nobare-400-1` | 6 | 2,52 … 2,54 s | 1 | 3,80 s |

`stdout` (de `bare-400-1`, campos de uso omitidos, todos zero):

```
{"type":"result","subtype":"success","is_error":true,"terminal_reason":"api_error",
 "api_error_status":400,"num_turns":1,"duration_ms":324,
 "result":"API Error: 400 forja recorder: request recorded", …}
```

## Consequência para o teto de tokens do proxy

| Resposta do proxy ao estourar o teto | Efeito medido no agente |
|---|---|
| 429 | continua tentando por 180 s (11 requisições, 1 amostra) e só então sai com exit 1; cada tentativa é uma requisição completa que o proxy tem de recusar de novo |
| 400 | sai com exit 1 em menos de 1,3 s (4 de 4) |
| 401 | continua tentando além de 120 s (3 de 3); fim não medido |

Em todos os casos o processo termina com `is_error: true` e
`api_error_status` igual ao status do proxy, e o `result` carrega a mensagem
do corpo de erro. O código de saída é 1 nos dois casos em que o processo
terminou sozinho (400 e 429).
