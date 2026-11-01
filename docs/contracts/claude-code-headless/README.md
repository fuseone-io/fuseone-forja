# Contrato observado: Claude Code headless → API do modelo

Gravações do protocolo HTTP que o Claude Code em modo headless (`claude -p`)
fala com o endereço em `ANTHROPIC_BASE_URL`. É o que o proxy de modelo da
Forja (`internal/proxy/model`) precisa aceitar, repassar e medir. Nada aqui
é lido da documentação: cada linha veio de uma requisição recebida por um
listener local nesta sessão.

Documentos:

| Documento | O que registra |
|---|---|
| [requisicao-messages.md](requisicao-messages.md) | Endpoints, método, query string, cabeçalhos e forma do corpo de `POST /v1/messages` |
| [erros-e-retentativas.md](erros-e-retentativas.md) | O que o agente faz quando o proxy responde 400, 401 e 429 — cronologia das retentativas e código de saída |
| [streaming-e-usage.md](streaming-e-usage.md) | Resposta SSE mínima que o agente aceita, onde o bloco `usage` aparece e o JSON que o `--output-format json` devolve |
| [recorder.py](recorder.py) | O gravador: listener HTTP + execução do `claude -p` sob o mesmo processo, para reproduzir as medições |

## Método

- **Versão medida:** `claude --version` → `2.1.316 (Claude Code)`, instalado
  em `/Users/phb/.local/bin/claude`. `User-Agent` observado:
  `claude-cli/2.1.316 (external, sdk-cli)`.
- **Listener:** `recorder.py` sobe um `ThreadingHTTPServer` em
  `127.0.0.1:<porta aleatória>`, registra método, caminho, cabeçalhos (valores
  de `x-api-key`, `authorization` e `cookie` redigidos para comprimento e
  igualdade com o token do run) e a forma do corpo JSON (chaves, tamanhos,
  nomes de ferramentas), e responde com o status pedido na linha de comando.
- **Ambiente do agente:** o gravador apaga todas as variáveis `ANTHROPIC_*`,
  `CLAUDECODE` e `CLAUDE_CODE_ENTRYPOINT` do ambiente herdado e define só
  `ANTHROPIC_BASE_URL=http://127.0.0.1:<porta>` e
  `ANTHROPIC_API_KEY=forja-run-token-<rótulo>` — um token opaco, não uma chave
  real. O diretório de trabalho é um workspace descartável com um único
  `README.md` de 107 bytes.
- **Tarefa:** `"List the files in this directory and say what README.md
  contains."` em todas as gravações.
- **Flags padrão do agente:** `-p --bare --output-format json
  --permission-mode dontAsk --permission-prompts none --tools
  Read,Edit,Write,Bash,Glob,Grep --max-budget-usd 1`. As variações estão em
  cada documento.
- **Onde rodou:** laptop do mantenedor (macOS, arm64), não um container. O
  listener e o agente estavam na mesma máquina; latência de rede não entra em
  nenhum número.

Comando que produz uma gravação (um processo, em primeiro plano):

```
python3 -I recorder.py <dir-saida> <rótulo> <workspace> <status> [timeout_s] -- \
  -p --bare --output-format json --permission-mode dontAsk --permission-prompts none \
  --tools Read,Edit,Write,Bash,Glob,Grep --max-budget-usd 1 \
  "List the files in this directory and say what README.md contains."
```

## Gravações desta sessão

11 gravações, 66 `POST` ao todo, todas em 2026-11-01.

| Rótulo | Status devolvido | `POST`s | Saída do `claude` | Tempo total |
|---|---|---|---|---|
| `bare-1` | 401 | 9 | encerrado pelo gravador aos 120 s, ainda retentando | 120,01 s |
| `bare-401-2` | 401 | 9 | idem | 120,01 s |
| `bare-401-3` | 401 | 9 | idem | 120,02 s |
| `bare-429-1` | 429 (`retry-after: 1`) | 9 | encerrado pelo gravador aos 120 s, ainda retentando | 120,02 s |
| `bare-429-long-1` | 429 (`retry-after: 1`) | 11 | exit 1 por conta própria, `api_error_status: 429` | 180,61 s |
| `bare-400-1` | 400 | 3 | exit 1, `api_error_status: 400` | 1,29 s |
| `bare-400-raw-1` | 400 | 3 | exit 1 | 1,23 s |
| `bare-default-tools-400-1` (sem `--tools`) | 400 | 3 | exit 1 | 0,85 s |
| `bare-restricted-400-1` (com `--restricted`) | 400 | 3 | exit 1 | 1,10 s |
| `bare-200-1` | 200 SSE falso | 1 | exit 0, `result: "forja-recorder-ok"` | 0,87 s |
| `nobare-400-1` (sem `--bare`) | 400 | 6 | exit 1 | 3,80 s |

## Afirmações checadas

| Afirmação | Resultado | Evidência |
|---|---|---|
| `ANTHROPIC_BASE_URL` é honrado | **confirmado** | 11 de 11 gravações: todas as requisições chegaram ao listener em `127.0.0.1`; nenhuma saída para `api.anthropic.com` era possível sem a variável e nenhuma gravação ficou vazia |
| `ANTHROPIC_API_KEY` é enviado tal qual, em `x-api-key` | **confirmado** | 66 de 66 `POST` com `x-api-key` igual, byte a byte, ao token `forja-run-token-<rótulo>` do run; nenhum `authorization` |
| O agente faz streaming | **confirmado** | 66 de 66 `POST` com `"stream": true` no corpo; a resposta SSE falsa de `bare-200-1` foi aceita e o agente saiu com exit 0 |
| `anthropic-version` e `anthropic-beta` são enviados | **confirmado** | `anthropic-version: 2023-06-01` em 66 de 66; `anthropic-beta` em 66 de 66 (lista em `requisicao-messages.md`) |
| O agente chama `POST /v1/messages/count_tokens` | **não observado** | 0 de 66 requisições; os únicos caminhos vistos foram `HEAD /api/hello` e `POST /v1/messages?beta=true`. Não medido em sessão longa (compactação) nem com `--autocompact` — todas as gravações terminaram na primeira resposta útil |
| `--bare` basta para o agente não ler configuração do host | **confirmado para o que foi medido** | Com `--bare`, 3 ferramentas e 4 470 bytes de corpo; sem `--bare`, 12 e depois 51 ferramentas (27 delas `mcp__*` do host) e 179 267 bytes. Sem `--bare` o `stderr` avisou: `claude.ai connectors are disabled because ANTHROPIC_API_KEY or another auth source is set and takes precedence over your claude.ai login` |
| O proxy pode negar com 429 e o agente para | **refutado como "para na hora"** | 429 é retentado: 11 tentativas em 180,0 s antes do exit 1 (1 amostra completa; 1 amostra de 120 s cortada com 9 tentativas). 400 **não** é retentado: exit 1 em menos de 1,3 s em 4 de 4 gravações |

## Não medido

- Tokens, minutos e taxa de gates verdes de um run real: exigem o proxy e o
  repositório piloto. Nenhuma gravação chegou a um modelo.
- Comportamento em container sem credencial: o Docker local existe
  (`docker info --format '{{.ServerVersion}} {{.OperatingSystem}}'` →
  `29.5.2 Ubuntu 24.04.4 LTS`), mas rodar o agente lá sem o proxy repetiria
  as mesmas gravações com latência a mais.
- `count_tokens`, compactação e `--autocompact`: ver tabela acima.
- Por que `--tools Read,Edit,Write,Bash,Glob,Grep` resultou em apenas `Bash`,
  `Edit` e `Read` no array `tools` (3 de 6 pedidas, em 9 de 9 gravações com
  `--bare` e `--tools`; sem `--tools`, as mesmas 3): observado, causa não
  medida.
