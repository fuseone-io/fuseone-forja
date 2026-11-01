# ADR 0027 — Repositório piloto e mantenedor da fase 0 (default provisório)

- **Status:** proposto — default tomado pelo time; **ratificação pendente do humano**
- **Data:** 2026-11-01
- **Issue:** Refs #1 (fica aberta até a ratificação)
- **Base medida:** `origin/main` em `f3d380573fee9340e9d798f3977513b768fc478c`

## Contexto

O PRD condiciona a aprovação da fase 0 à escolha de um repositório piloto **de
outra área** e do seu mantenedor ("Repositório piloto: a definir — condição
para aprovar"). O critério do PRD para esse repositório é: testes medianos de
propósito (para expor o risco "PR verde e errada"), gates executáveis por
comando (build, lint, teste) e ao menos um teste de integração com serviço real
(ex.: Postgres) para exercitar `services`.

Essa escolha é humana: envolve o consentimento de outra área e a nomeação de
uma pessoa. O humano pediu que o time seguisse sem interrupções. Este ADR,
portanto, **não decide o piloto**: registra o default que permite construir e
exercitar a fase 0 de ponta a ponta enquanto a decisão real não chega, e deixa
explícito o que só o humano pode fazer.

## Decisão (provisória)

| Item | Default tomado |
|---|---|
| Repositório piloto | `fuseone-io/fuseone-forja` — este repositório (dogfooding) |
| Mantenedor | Paulo Bortolotti, dono do repositório |
| Contrato | `.forja.yaml` na raiz, na forma v1 do PRD, adaptado a este repositório |

Por que este default e não outro: é o único repositório que o time consegue
medir e rodar de ponta a ponta sem credencial de terceiros nem consentimento de
outra área. Ele dá às fatias seguintes (parser do contrato, `forja.doctor`,
runner de gates) um contrato real para validar, em vez de um exemplo
inventado.

### O que este default **não** cumpre do critério do PRD

| Critério do PRD | Este repositório hoje | Consequência |
|---|---|---|
| Repositório de **outra área** | É o repositório da própria Forja | O default não serve para aprovar a fase 0; serve para construí-la |
| Testes medianos de propósito | 19 testes unitários em 4 pacotes, todos passando (ver inventário) | Cobertura ainda pequena, mas é código novo: não há "teste mediano" deliberado |
| Teste de integração com serviço real | Nenhum (ver "Serviços de apoio") | `services` e `needs:` do contrato não são exercitados por este piloto hoje |

O piloto real continua sendo decisão do humano. Quando ele for nomeado, este
ADR é substituído por outro que registra o repositório, o mantenedor e o
inventário daquele repositório, medido pelo mesmo método.

## Inventário dos gates de hoje

Fonte: `Makefile` em `origin/main`. Cinco alvos de gate e um agregador.

| Gate | Comando (Makefile) | Agregado por |
|---|---|---|
| build | `go build -o bin/forjad ./cmd/forjad` | `make check` |
| vet | `go vet ./...` | `make check` |
| lint | `golangci-lint run ./...` | `make check` |
| test | `go test ./...` | `make check` |
| race | `go test -race ./...` | `make check` |

A CI (`.github/workflows/check.yml`) roda `make build`, `make vet`,
`make test` e `make race` com `actions/setup-go@v7` lendo `go.mod`, e roda o
lint pela action `golangci/golangci-lint-action@v9` com `version: v2.15.0`
— não pelo alvo `make lint`.

### Duração aproximada — 3 execuções por gate

**Ambiente da medição:** laptop do mantenedor, `go1.27.2 darwin/arm64`, cache
de build e de módulos quentes, worktree limpa em `f3d3805`. Não é o ambiente
da Forja (container efêmero, cache frio); os números abaixo são o piso, não a
estimativa do run.

Observação: `go.mod` declara `go 1.25.0`, mas o toolchain local é `go1.27.2`
(saída de `go version`). A imagem do contrato é `golang:1.25`, alinhada ao
`go.mod`.

| Gate | Comando medido | Execução 1 | Execução 2 | Execução 3 | Exit |
|---|---|---|---|---|---|
| build | `/usr/bin/time -p make build` | 0,50 s | 0,36 s | 0,21 s | 0 / 0 / 0 |
| vet | `/usr/bin/time -p make vet` | 0,27 s | 0,19 s | 0,19 s | 0 / 0 / 0 |
| lint (como está no Makefile) | `/usr/bin/time -p make lint` | 0,04 s | — | — | **2** (ver abaixo) |
| lint (binário explícito v2.15.0) | `/usr/bin/time -p <scratch>/golangci-lint run ./...` | 1,07 s | 0,41 s | 0,42 s | 0 / 0 / 0 |
| test (como está no Makefile; resultado em cache) | `make test` sob `perl -MTime::HiRes` | 0,34 s | 0,21 s | 0,20 s | 0 / 0 / 0 |
| test sem cache | `go test -count=1 ./...` sob `perl -MTime::HiRes` | 0,74 s | 0,42 s | 0,43 s | 0 / 0 / 0 |
| race (como está no Makefile) | `make race` sob `perl -MTime::HiRes` | 2,33 s | 0,19 s | 0,20 s | 0 / 0 / 0 |
| race sem cache | `go test -race -count=1 ./...` sob `perl -MTime::HiRes` | 1,85 s | 1,52 s | 1,52 s | 0 / 0 / 0 |
| check | `make check` | não medido como alvo único: localmente falha em `lint` pelo mesmo erro abaixo; a soma dos cinco gates sem cache fica abaixo de 5 s | — |

`make lint` falhou localmente com o erro literal:

```
golangci-lint run ./...
make: golangci-lint: No such file or directory
make: *** [lint] Error 1
```

O alvo depende de `golangci-lint` no `PATH`. No workspace da Forja isso vira
um passo de `setup` (ver "Contrato"). Para medir o lint, o binário v2.15.0 foi
chamado pelo caminho explícito (`golangci-lint has version 2.15.0 built with
go1.27.2`).

Por que `make test` e `make race` foram medidos sob `perl -MTime::HiRes` e não
sob `/usr/bin/time`: o sandbox desta sessão recusou `/usr/bin/time -p make
test` ("runs time with the text test in a plain command"); o cronômetro em
Perl mede o mesmo tempo de parede, com resolução de centésimos.

### Testes existentes

`rtk proxy go test -list '.*' ./...` listou **19 funções de teste em 4
pacotes**:

| Pacote | Testes |
|---|---|
| `cmd/forjad` | 2 |
| `internal/domain` | 2 |
| `internal/mcp` | 7 |
| `internal/runstore` | 8 |

## Serviços de apoio necessários

**Nenhum hoje.**

- `/usr/bin/git grep -n -i -e postgres -e integration -- '*.go' Makefile go.mod`
  saiu com código 1 (nenhuma ocorrência).
- `internal/runstore` contém apenas `memory.go` e `memory_test.go`: o run store
  é em memória.
- Não existe alvo `integration-test` no Makefile.

Um container Postgres real passa a ser necessário quando o run store ganhar o
teste de integração previsto no layout do `CLAUDE.md`. Nesse momento o
contrato recebe `services.postgres` e o gate correspondente recebe
`needs: [postgres]`; este ADR não antecipa isso.

Docker local disponível para a medição: `docker info` → `29.5.2 Ubuntu 24.04.4
LTS aarch64` (VM do Docker Desktop). Isso descreve o laptop, não o runtime da
Forja.

## Registries privados consumidos pelo setup

**Nenhum.**

- `go.mod` requer diretamente apenas `github.com/modelcontextprotocol/go-sdk
  v1.8.0`; os indiretos são módulos públicos.
- `rtk proxy go env GOPRIVATE` → vazio.
- `go env GOPROXY` → `https://proxy.golang.org,direct`.

O `setup` do contrato resolve módulos e instala o `golangci-lint` via
`proxy.golang.org`. Isso é egress para um registry **público**, não privado;
como o workspace só alcança os proxies do plano de controle, a fatia do proxy
de registries precisa decidir se `proxy.golang.org` passa por ele. Fica
registrado aqui como dependência do `setup`, não como decisão.

## Contrato `.forja.yaml`

Escrito na forma v1 do PRD, adaptado a este repositório. Resumo das escolhas:

| Campo | Valor | Por quê |
|---|---|---|
| `image` | `golang:1.25` | Alinhado ao `go 1.25.0` do `go.mod`. `docker manifest inspect golang:1.25` confirmou a imagem no registry público (digest `sha256:54b6b88db6fe375c6676625d87d668273f85c6d09153635d0cbba89cba7a207a` para `linux/amd64`) |
| `setup` | `go mod download`; `go install .../golangci-lint@v2.15.0` | O lint exige o binário; a versão é a mesma que a CI fixa |
| `services` | `{}` | Nenhum serviço hoje |
| `registries` | `[]` | Nenhum registry privado |
| `gates` | build, vet, lint, test, race | Os cinco alvos do Makefile, na ordem de `make check` |
| `verify.exclude` | `**/*.md`, `docs/**` | Diff só em documentação é "não verificável" |
| `proof_paths` | `.forja.yaml`, `Makefile`, `.golangci.yml`, `**/testdata/**`, `**/*_test.go` | O que altera a prova; `.golangci.yml` existe na raiz e configura o lint |
| `human_merge_paths` | `.github/**` | Mesmo valor do PRD |
| `limits` | `wall_clock: 30m`, `max_diff_lines: 400` | Mesmos valores do PRD; os gates medidos ficam muito abaixo |
| `agent_context` | `CLAUDE.md`, `CONTRIBUTING.md`, `docs/` | O que o agente lê antes de codar |

Ponto em aberto medido, que o `forja.doctor` vai expor: o `go.mod` do
`golangci-lint v2.15.0` declara `go 1.26.0` (obtido com `curl -sS
https://proxy.golang.org/github.com/golangci/golangci-lint/v2/@v/v2.15.0.mod`).
Sob `golang:1.25`, `go install ...@v2.15.0` depende do `GOTOOLCHAIN=auto`
baixar um toolchain 1.26 — **não medido** nesta sessão. Se o doctor ficar
vermelho nesse passo, as saídas são: imagem `golang:1.26`, uma versão do
`golangci-lint` compatível com 1.25, ou uma imagem que já traga o binário. A
escolha é do mantenedor.

A validação da forma do contrato (campos, tipos, defaults) é da issue #6; este
ADR não a antecipa. Se `agent_context` não aceitar diretório, `docs/` vira a
lista dos arquivos relevantes.

## O que só o humano pode fazer

- [ ] Ratificar este default como piloto **de construção**, ou nomear desde já
      o repositório de outra área e seu mantenedor.
- [ ] Nomear o repositório piloto **real** (outra área), com testes medianos de
      propósito e ao menos um teste de integração com serviço real, e o seu
      mantenedor — condição do PRD para aprovar a fase 0.
- [ ] Confirmar que o mantenedor nomeado sabe que escreverá o `.forja.yaml`
      daquele repositório e rodará `forja.doctor` até ficar verde na `main`.
- [ ] Decidir se este repositório continua como segundo piloto (dogfooding)
      depois que o piloto real existir.
- [ ] Decidir a saída para o `setup` do lint sob `golang:1.25` (ponto em aberto
      acima), quando o `forja.doctor` existir e apontar o resultado.

## Comandos que produziram os números

Todos rodados nesta sessão, na worktree em `f3d3805`, em 2026-11-01.

```
/usr/bin/git rev-parse origin/main
go version
<scratch>/golangci-lint --version
gh repo view fuseone-io/fuseone-forja --json owner,defaultBranchRef,visibility,isPrivate
/usr/bin/git config user.name
rtk proxy go test -list '.*' ./...
/usr/bin/git grep -n -i -e postgres -e integration -- '*.go' Makefile go.mod
rtk proxy go env GOPRIVATE
go env GOPRIVATE GOPROXY GONOSUMDB GOFLAGS
docker info --format '{{.ServerVersion}} {{.OperatingSystem}} {{.Architecture}}'
docker manifest inspect golang:1.25 --verbose
curl -sS https://proxy.golang.org/github.com/golangci/golangci-lint/v2/@v/v2.15.0.mod
/usr/bin/time -p make build        (x3)
/usr/bin/time -p make vet          (x3)
/usr/bin/time -p make lint         (x1, exit 2)
/usr/bin/time -p <scratch>/golangci-lint run ./...   (x3)
perl -MTime::HiRes=time -e '... system("make","test") ...'                         (x3)
perl -MTime::HiRes=time -e '... system("go","test","-count=1","./...") ...'        (x3)
perl -MTime::HiRes=time -e '... system("make","race") ...'                         (x3)
perl -MTime::HiRes=time -e '... system("go","test","-race","-count=1","./...") ...' (x3)
```

`<scratch>` é o diretório temporário da sessão onde o binário do
`golangci-lint` v2.15.0 estava instalado; ele não faz parte do repositório.
