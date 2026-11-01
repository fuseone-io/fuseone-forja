# ADR 0029 — Agente que roda no workspace (default provisório)

- **Status:** proposto — default tomado pelo time; **ratificação pendente do humano**
- **Data:** 2026-11-01
- **Issue:** Refs #2 (fica aberta até a ratificação)
- **Base medida:** `origin/main` em `ce95147dea46427850e944c22186195fb9e23c89`
- **Gravações:** [`docs/contracts/claude-code-headless/`](../contracts/claude-code-headless/README.md)

## Contexto

O PRD coloca na fase 0 um spike para escolher o agente que roda dentro do
workspace: **Claude Code headless** ou **um laço próprio sobre a API do
modelo**. A escolha decide três coisas que o resto da Forja herda: o custo por
run (tokens e minutos), o controle sobre o que o agente consegue executar e
alcançar, e a forma do proxy de modelo (`internal/proxy/model`) — protocolo,
streaming, cabeçalhos e o mecanismo do teto de tokens.

A issue #2 pede as duas opções rodando a mesma issue, em container sem
credencial, três vezes cada, com tabela de tokens, minutos, gates verdes e
intervenções. Isso exige o proxy de modelo e o repositório piloto, que ainda
não existem (ADR 0027 registra o piloto como default provisório). O humano
pediu que o time seguisse sem interrupções. Este ADR, portanto, registra o
**default** tomado, mede em modo somente leitura o que o proxy precisa
suportar — gravando o protocolo real que o Claude Code fala — e deixa a
tabela comparativa com as células que não puderam ser medidas marcadas
`não medido`, nunca estimadas.

## Decisão (default tomado)

### Agente: Claude Code headless

`claude -p` em modo `--bare`, saída `--output-format json`, ferramentas
restritas ao workspace, sem persistência de sessão. Forma de invocação que o
driver (`internal/agent`) usa, com as flags que o `claude --help` da versão
`2.1.316` lista:

```
ANTHROPIC_BASE_URL=http://<proxy-de-modelo-do-run> \
ANTHROPIC_API_KEY=<token opaco do run> \
claude -p --bare \
  --output-format json \
  --no-session-persistence \
  --permission-mode dontAsk --permission-prompts none \
  --tools Read,Edit,Write,Bash,Glob,Grep \
  --max-budget-usd <teto em USD, redundante ao do proxy> \
  --append-system-prompt "<agent_context renderizado pelo plano de controle>" \
  "<título e corpo da issue, critérios de aceite, gates do contrato>"
```

Por que `--bare`: medido em
[requisicao-messages.md](../contracts/claude-code-headless/requisicao-messages.md),
sem `--bare` o agente carregou a configuração do host (12 e depois 51
ferramentas, 27 delas de servidores MCP configurados fora do workspace, corpo
de 179 267 bytes) e avisou que `ANTHROPIC_API_KEY` tem precedência sobre o
login do claude.ai. Com `--bare`, o prompt de sistema tem 3 blocos (billing
header, uma frase de identidade e `CWD`/`Date`) e o corpo 4 470 bytes:
nenhum hook, nenhum `CLAUDE.md` descoberto, nenhum keychain. Dentro do
workspace, "configuração do projeto" seria a árvore do agente configurando o
próprio agente — `--bare` fecha esse caminho.

Por que `--permission-prompts none` e `--permission-mode dontAsk`: não há
humano para responder; o que exigiria aprovação é negado e aparece em
`permission_denials` no JSON final.

### Alternativa rejeitada para a fase 0: laço próprio sobre a API

Um laço próprio fala o **mesmo** protocolo com o proxy — `POST /v1/messages`
com `stream: true`, `tools`, `tool_use`/`tool_result` — e, portanto, não
muda a forma do proxy. O que ele muda é quem reconstrói: execução de
ferramentas (shell, leitura e edição de arquivos com diff aplicado), a
política de permissões, a gestão de contexto (compactação, limpeza de
`thinking`), as retentativas e o relatório de uso. O Claude Code já entrega
tudo isso e foi medido falando com um listener local sem nenhuma adaptação.
Rejeitado pelo custo de reconstruir controle de ferramentas; **não medido**
porque não foi construído. Volta à mesa se **uma** destas condições ocorrer:

1. a tabela comparativa, quando medida, mostrar que o Claude Code gasta mais
   de 2× os tokens de um laço mínimo na mesma issue; ou
2. uma versão do Claude Code mudar o protocolo de forma que o proxy não
   consiga mais medir `usage` (por exemplo, deixar de enviar
   `message_delta.usage`).

## O que o proxy de modelo precisa suportar

Tudo abaixo foi observado em 11 gravações, 66 `POST`, nesta sessão
([README.md](../contracts/claude-code-headless/README.md)). Nada foi assumido
de documentação.

| Item | Observado | O que o proxy faz |
|---|---|---|
| `ANTHROPIC_BASE_URL` | honrado em 11 de 11 gravações | É o endereço do proxy na rede `--internal` do run (ADR 0026) |
| `HEAD /api/hello` | 1 por processo, antes da primeira mensagem, sem `x-api-key` | Responde (qualquer status serviu: 200, 400, 401 e 429 foram testados) |
| `POST /v1/messages?beta=true` | 66 de 66 chamadas ao modelo; `count_tokens` 0 de 66 | Repassa ao upstream com a chave real; preserva a query string |
| `x-api-key` | igual ao `ANTHROPIC_API_KEY` do run, byte a byte, em 66 de 66 | Token opaco por run: o proxy o resolve para a chave real e o valida contra o run ativo; a chave real nunca entra no workspace |
| `anthropic-version: 2023-06-01` | 66 de 66 | Repassa |
| `anthropic-beta` | 7 itens em `--bare`; varia por flag e por retentativa | Repassa sem filtrar |
| `stream: true` | 66 de 66 | Repassa SSE em `text/event-stream`, evento a evento; não bufferiza |
| `role: "system"` dentro de `messages` | 66 de 66 (mensagem de `output_config.effort`) | Aceita e repassa; o proxy não valida o corpo |
| `x-stainless-timeout: 600` | 66 de 66 | Não corta antes de 600 s; o `wall_clock` do run é quem encerra |
| `usage` | `message_start.message.usage.input_tokens` e `message_delta.usage.output_tokens`, ecoados em `stdout` tal qual enviados (1234 / 56) | É de onde o proxy mede |
| `metadata.user_id` | JSON com `device_id`, `account_uuid` vazio e `session_id` | Não é credencial; pode ser substituído pelo id do run para correlação |

Lista completa de cabeçalhos e a forma do corpo:
[requisicao-messages.md](../contracts/claude-code-headless/requisicao-messages.md).
Resposta SSE mínima aceita e JSON de saída:
[streaming-e-usage.md](../contracts/claude-code-headless/streaming-e-usage.md).

## Teto de tokens

### Mecanismo

1. O plano de controle emite **um token opaco por run** e o entrega ao
   workspace em `ANTHROPIC_API_KEY`. O proxy mapeia token → run → chave real.
2. A cada resposta que repassa, o proxy soma ao run
   `message_start.message.usage.input_tokens` +
   `cache_creation_input_tokens` + `cache_read_input_tokens` e
   `message_delta.usage.output_tokens`. O total é persistido no
   `runstore` como `domain.Cost` (inteiros).
3. Quando o total ultrapassa `limits` do contrato, o proxy **recusa com 429**
   e corpo `{"type":"error","error":{"type":"rate_limit_error","message":
   "forja: run token cap reached"}}`, sem chamar o upstream.
4. Ao recusar, o proxy avisa o plano de controle, que **revoga o token** e
   encerra o workspace. O teto é aplicado pelo plano, não pela cooperação do
   agente.

O `--max-budget-usd` da linha de comando é redundância do lado do agente, não
evidência: o workspace controla o processo que o calcula.

### O que a medição mostrou sobre o 429

[erros-e-retentativas.md](../contracts/claude-code-headless/erros-e-retentativas.md):
o Claude Code **retenta** o 429 com recuo exponencial — 11 requisições em
180,0 s antes de sair com exit 1 (1 amostra completa; outra amostra cortada
aos 120 s com 9 requisições) — e `retry-after: 1` não encurtou o recuo. Um
400 **não** é retentado: exit 1 em menos de 1,3 s em 4 de 4 gravações.

Consequências para o passo 4 acima: sem a revogação pelo plano, o workspace
ficaria até 180 s martelando o proxy depois do teto (barato — nenhuma chamada
ao upstream — mas tempo de parede gasto). Com a revogação e o encerramento
do workspace, o 429 vira só o registro do motivo. A alternativa — responder
400 para o agente parar sozinho em ~1 s — fica para o humano decidir (ver
checklist); 429 é o default porque é o status que descreve o fato e porque o
`api_error_status` do JSON final o preserva.

## O que o agente lê e o que executa

### Lê (entrada que o plano de controle monta)

| Fonte | Como chega ao agente | Observação |
|---|---|---|
| Árvore do repositório no branch do run | checkout feito pelo plano de controle no workspace; o agente usa `Read` e `Bash` | É a única coisa que o agente lê por conta própria |
| `agent_context` do contrato (`.forja.yaml` do commit base; hoje `CLAUDE.md`, `CONTRIBUTING.md`, `docs/`) | renderizado pelo plano em `--append-system-prompt`, ou lista de caminhos no prompt para o agente ler | Medido: em `--bare` não há descoberta de `CLAUDE.md`; o que não for passado explicitamente não chega |
| Título, corpo e critérios de aceite da issue | no prompt, pelo plano de controle | O plano não loga o corpo (dado pessoal); referencia o número |
| Comandos dos gates do contrato (`make build`, `make vet`, `make lint`, `make test`, `make race`) | no prompt, como "o que vai ser cobrado" | O agente pode rodá-los com `Bash` para iterar; o gate de registro é o do plano, fora do workspace |

Não lê: `.forja.yaml` como autoridade (o contrato vem do commit base, regra 2
do `CLAUDE.md`); credenciais (não existem no workspace, regra 4); a evidência
(escrita fora, regra 3).

### Executa (ferramentas no array `tools`)

| Ferramenta | Pedida | Enviada ao modelo (medido) | Alcance |
|---|---|---|---|
| `Bash` | sim | sim (schema de 1 616 bytes) | Qualquer comando dentro do workspace; rede só até os proxies (ADR 0026) |
| `Edit` | sim | sim | Edição in-place de arquivos do workspace |
| `Read` | sim | sim | Leitura de arquivos do workspace |
| `Write`, `Glob`, `Grep` | sim | **não apareceram** no array em 9 de 9 gravações | Observado; causa não medida (o agente pode cobri-las com `Bash`) |

Não consegue: fazer push (nenhuma credencial de git), escrever `forja/gates`
ou `forja/qa` (ficam fora do workspace), alcançar qualquer host além dos
proxies, ler a chave real do modelo (só vê o token opaco).

## Tabela comparativa pedida pela issue

Nenhuma célula foi medida: as duas opções exigem o proxy de modelo (para
chegar a um modelo sem credencial no workspace) e o repositório piloto (para
a mesma issue e os mesmos gates). O laço próprio não foi construído.

| Opção | Tokens (input+output) | Minutos | Gates verdes | Intervenções | Runs |
|---|---|---|---|---|---|
| Claude Code headless | não medido | não medido | não medido | não medido | 0 de 3 |
| Laço próprio sobre a API | não medido | não medido | não medido | não medido | 0 de 3 |

O que a sessão mediu em lugar disso — o protocolo, as retentativas e o
caminho do `usage` — está nas gravações. Quando o proxy existir, a tabela se
preenche rodando `recorder.py` com status 200 trocado por repasse real, ou
diretamente o driver, três vezes por opção, no container do ADR 0026.

## Consequências

- `internal/proxy/model` é um proxy HTTP reverso da Messages API com três
  obrigações: trocar `x-api-key` pela chave real, repassar SSE sem bufferizar
  e ler `usage` em dois eventos. Não precisa validar corpo nem conhecer
  ferramentas.
- `internal/agent` é um driver de processo: monta o prompt, define duas
  variáveis de ambiente, roda `claude -p --bare …` com o `wall_clock` como
  deadline do `context` e lê **um** JSON de `stdout` como informação (nunca
  como evidência).
- O teto de tokens vive no proxy e no `runstore`; o agente só o percebe como
  429. O plano encerra o workspace ao recusar — sem isso, até 180 s de
  retentativas (medido).
- Toda versão nova do Claude Code é uma mudança de protocolo em potencial. O
  `recorder.py` reexecutado contra a nova versão é o teste de regressão do
  contrato; a lista de `anthropic-beta` já variou por flag nesta versão.
- O custo real por run continua desconhecido até a tabela ser preenchida; a
  fatia do proxy não deve ser dimensionada por estimativa.

## O que só o humano pode fazer

- [ ] Ratificar o Claude Code headless (`--bare`) como agente da fase 0, ou
      escolher o laço próprio. Ao ratificar, trocar o status para `aceito`.
- [ ] Decidir o status do teto: 429 (default, descreve o fato, até 180 s de
      retentativas sem a revogação) ou 400 (o agente para em ~1 s, status
      semanticamente errado).
- [ ] Autorizar o gasto para preencher a tabela comparativa: 3 runs por opção
      na mesma issue do piloto, via proxy, em container — tokens reais.
- [ ] Decidir se o laço próprio é construído como alternativa de medição
      (para preencher a sua linha) ou se a linha fica `não medido` até uma
      das condições de revisita.
- [ ] Confirmar o modelo e o `effort` que o driver fixa (`--model`,
      `--effort`): a medição usou o default da versão, `claude-opus-5-5`
      com `effort: high`.

## Comandos que produziram os números

Todos rodados nesta sessão, em 2026-11-01, no laptop do mantenedor (macOS,
arm64), com o gravador e o agente na mesma máquina.

```
/Users/phb/.local/bin/claude --version
/Users/phb/.local/bin/claude --help
/usr/bin/git rev-parse origin/main
docker info --format '{{.ServerVersion}} {{.OperatingSystem}}'
python3 -I recorder.py <dir> bare-1 <ws> 401 -- -p --bare --output-format json --permission-mode dontAsk --permission-prompts none --tools Read,Edit,Write,Bash,Glob,Grep --max-budget-usd 1 "List the files in this directory and say what README.md contains."
python3 -I recorder.py <dir> bare-401-2 <ws> 401 120 -- (mesmas flags)
python3 -I recorder.py <dir> bare-401-3 <ws> 401 120 -- (mesmas flags)
python3 -I recorder.py <dir> bare-429-1 <ws> 429 -- (mesmas flags)
python3 -I recorder.py <dir> bare-429-long-1 <ws> 429 560 -- (mesmas flags)
python3 -I recorder.py <dir> bare-400-1 <ws> 400 -- (mesmas flags)
python3 -I recorder.py <dir> bare-400-raw-1 <ws> 400 120 -- (mesmas flags)
python3 -I recorder.py <dir> bare-default-tools-400-1 <ws> 400 -- (mesmas flags sem --tools)
python3 -I recorder.py <dir> bare-restricted-400-1 <ws> 400 -- (mesmas flags mais --restricted)
python3 -I recorder.py <dir> bare-200-1 <ws> 200 120 -- (mesmas flags)
python3 -I recorder.py <dir> nobare-400-1 <ws> 400 -- -p --output-format json --permission-mode dontAsk --permission-prompts none --max-budget-usd 1 "List the files in this directory and say what README.md contains."
```

`<dir>` e `<ws>` são diretórios temporários da sessão; as gravações JSON
brutas (com o corpo completo, inclusive o caminho do workspace) ficaram fora
do repositório. O `recorder.py` está em
`docs/contracts/claude-code-headless/`.

## Referências

- `docs/PRD.pdf` — Forja: runtime de execução da fábrica de software.
- `CLAUDE.md` — regras 2 (contrato do commit base), 3 (evidência pelo plano
  de controle), 4 (nenhuma credencial no workspace).
- ADR 0026 — isolamento do workspace (rede `--internal` por run e proxies).
- ADR 0027 — repositório piloto (default provisório) e `agent_context` do
  `.forja.yaml`.
- Issue #2.
