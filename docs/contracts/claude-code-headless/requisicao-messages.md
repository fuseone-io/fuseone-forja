# Requisição `POST /v1/messages` como o Claude Code headless a envia

Fonte: gravação `bare-400-raw-1` (corpo bruto guardado) cruzada com as outras
10 gravações de [README.md](README.md). Versão `2.1.316`. Valores secretos
redigidos; identificadores de sessão substituídos por `<…>`.

## Endpoints vistos

Em 11 gravações e 77 requisições (11 `HEAD` + 66 `POST`), só dois caminhos:

| Ordem | Método | Caminho | Quando | Cabeçalhos relevantes |
|---|---|---|---|---|
| 1 | `HEAD` | `/api/hello` | uma vez, ao subir, antes da primeira mensagem | `user-agent: Bun/1.4.4`; **sem** `x-api-key`, sem `anthropic-version` |
| 2… | `POST` | `/v1/messages?beta=true` | toda chamada ao modelo (66 de 66) | tabela abaixo |

O `HEAD /api/hello` é uma sondagem de conectividade: o gravador respondeu
401/400/429/200 a ele e o agente seguiu para o `POST` em todos os casos (11 de
11). O proxy precisa **responder** a ele; o status não importou nas gravações.

`POST /v1/messages/count_tokens`: 0 ocorrências em 66 `POST`. Ver README.

## Cabeçalhos do `POST /v1/messages`

Como chegaram ao listener (ordem do cliente), gravação `bare-1`, primeira
requisição:

```
accept: application/json
content-type: application/json
user-agent: claude-cli/2.1.316 (external, sdk-cli)
x-claude-code-session-id: <uuid da sessão>
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
x-api-key: <redigido — 22 bytes, igual ao ANTHROPIC_API_KEY do run>
x-app: cli
connection: keep-alive
host: 127.0.0.1:<porta>
accept-encoding: gzip, deflate, br, zstd
content-length: 4470
```

O que o proxy deve fazer com cada um:

| Cabeçalho | Observado | Para o proxy |
|---|---|---|
| `x-api-key` | igual ao valor de `ANTHROPIC_API_KEY`, byte a byte, em 66 de 66 `POST`; nunca em `authorization` | É o **token opaco do run**. O proxy o troca pela chave real e nunca o repassa |
| `anthropic-version` | `2023-06-01` em 66 de 66 | Repassar tal qual |
| `anthropic-beta` | lista com 7 itens em `--bare` (acima); 10 itens sem `--bare` (acrescenta `mid-conversation-tool-changes-2026-07-01`, `inline-tools-2026-09-15`, `advanced-tool-use-2025-11-20`); na retentativa pós-400 cai `thinking-token-count-2026-05-13` | Repassar tal qual; não filtrar — a lista varia por flag e por retentativa |
| `x-stainless-timeout` | `600` (segundos) | O proxy não pode cortar a conexão antes disso; o `wall_clock` do run é quem corta |
| `x-stainless-retry-count` | sempre `0`, mesmo na 11ª tentativa após 429 | As retentativas são do laço do Claude Code, não do SDK; o proxy não consegue distingui-las por este cabeçalho |
| `x-claude-code-session-id` | um UUID por processo | Não é credencial; útil para correlacionar o run nos logs do proxy |
| `accept-encoding` | `gzip, deflate, br, zstd` | Resposta sem compressão foi aceita (`bare-200-1`) |
| `anthropic-dangerous-direct-browser-access`, `x-app` | `true`, `cli` | Repassar |

## Corpo do `POST /v1/messages` (modo `--bare`)

4 470 bytes. Chaves de primeiro nível, em 66 de 66 `POST`:

```
context_management, max_tokens, messages, metadata, model, output_config, stream, system, thinking, tools
```

Valores observados:

| Campo | Valor em `bare-400-raw-1` |
|---|---|
| `model` | `claude-opus-5-5` (sem `--model`) |
| `stream` | `true` (66 de 66) |
| `max_tokens` | `128000` |
| `thinking` | `{"type": "adaptive", "display": "omitted"}`; ausente na 3ª variante pós-400 |
| `output_config` | `{"effort": "high"}`; com `--restricted` veio `"medium"` (1 gravação) |
| `context_management` | `{"edits": [{"type": "clear_thinking_20251015", "keep": "all"}]}` |
| `metadata` | `{"user_id": "<JSON com device_id, account_uuid vazio e session_id>"}` — identificador de dispositivo, não credencial |

### `system` — 3 blocos

| # | Tamanho | Conteúdo | `cache_control` |
|---|---|---|---|
| 0 | 74 chars | `x-anthropic-billing-header: cc_version=2.1.316.f93; cc_entrypoint=sdk-cli;` | — |
| 1 | 62 chars | `You are a Claude agent, built on Anthropic's Claude Agent SDK.` | `{"type": "ephemeral"}` |
| 2 | 160 chars | `CWD: <caminho do workspace>\nDate: 2026-11-01` | `{"type": "ephemeral"}` |

Em `--bare` o prompt de sistema não carrega `CLAUDE.md`, memória nem
instruções do host: só o que a linha de comando der (`--system-prompt`,
`--append-system-prompt`, `--add-dir`). É por isso que o `agent_context` do
contrato tem de ser entregue explicitamente (ver ADR).

### `messages` — 2 entradas

```
[{"role": "user", "content": [{"type": "text", "text": "<prompt>", "cache_control": {"type": "ephemeral"}}]},
 {"role": "system", "content": [], "output_config": {"effort": "high"}}]
```

A segunda entrada é a mensagem de sistema no meio da conversa que fixa o
`effort` por turno; o proxy precisa aceitar `role: "system"` dentro de
`messages`.

### `tools` — 3 ferramentas

Pedidas: `--tools Read,Edit,Write,Bash,Glob,Grep`. Enviadas:

| Nome | Bytes do schema | `description` |
|---|---|---|
| `Bash` | 1 616 | `execute shell commands` |
| `Edit` | 666 | `modify file contents in place` |
| `Read` | 1 200 | `read files, images, PDFs, notebooks` |

Mesmas 3 sem `--tools` e com `--restricted`. O schema de `Bash` tem
`command` (obrigatório), `timeout`, `description` e
`dangerouslyDisableSandbox`, com `additionalProperties: false`.

## Corpo sem `--bare` (gravação `nobare-400-1`)

Dois tamanhos no mesmo processo: 81 155 bytes com 12 ferramentas, depois
179 267 bytes com 51 ferramentas (24 nativas + 27 `mcp__*` dos servidores MCP
configurados **no host**). Ferramentas nativas vistas: `Agent`, `Bash`,
`CronCreate`, `CronDelete`, `CronList`, `DesignSync`, `Edit`, `EnterWorktree`,
`ExitWorktree`, `ListAgents`, `LSP`, `Monitor`, `NotebookEdit`,
`PushNotification`, `Read`, `ReportFindings`, `ScheduleWakeup`, `SendMessage`,
`Skill`, `TaskStop`, `WebFetch`, `WebSearch`, `Workflow`, `Write`.

Consequência para a Forja: sem `--bare` o agente carrega a configuração do
host e do projeto (`.claude/`, `.mcp.json`, hooks). Dentro do workspace isso
seria a árvore do agente configurando o próprio agente. `--bare` corta esse
caminho.

## Variantes pós-erro 400

Após um 400 o agente disparou, no mesmo instante (Δ ≤ 0,01 s), 3 `POST`:

1. idêntico ao original;
2. idêntico ao original;
3. sem a chave `thinking` e sem `thinking-token-count-2026-05-13` em
   `anthropic-beta` (4 450 bytes).

Depois saiu com exit 1. Em 4 de 4 gravações com 400 e `--bare`; sem `--bare`
foram 6 `POST` (3 com 12 ferramentas, 3 com 51).
