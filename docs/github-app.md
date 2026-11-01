# GitHub App da Forja — runbook de provisionamento

- **Tipo:** runbook (não é ADR: as permissões vêm do PRD; o que falta é executar)
- **Status:** default tomado pelo time; **provisionamento e ratificação pendentes do humano**
- **Issue:** Refs #4 (fica aberta até o App existir e estar instalado)
- **Data:** 2026-11-01
- **Base medida:** `origin/main` em `ce95147dea46427850e944c22186195fb9e23c89`
- **Repositório piloto assumido:** `fuseone-io/fuseone-forja` (default provisório do
  [ADR 0027](adr/0027-repositorio-piloto.md); se o piloto real for outro, este
  runbook se aplica a ele sem mudança, só troca o alvo da instalação e da ruleset)

## Por que este documento e não o App

Registrar um GitHub App exige a interface web do GitHub, como **owner da
organização** (ou membro, se a organização permitir que membros criem Apps —
configuração que não pôde ser lida nesta sessão; ver "Medições"), e a chave
privada gerada no registro precisa ir para o cofre que o plano de controle lê.
Nenhuma das duas coisas um agente pode fazer: não tem sessão web, não tem o
cofre, e não deve ter a chave.

O humano pediu que o time seguisse sem interrupções. Este runbook, portanto,
deixa pronto **tudo que é texto**: as permissões exatas, o manifesto para colar
no fluxo "Register a GitHub App from a manifest", o escopo da instalação, como o
plano de controle lê as credenciais, a ruleset que a `main` do piloto precisa e
os comandos que provam que o App não consegue empurrar na `main`. O que só o
humano pode fazer está no checklist ao fim.

## Permissões do App (mínimas)

Uma instalação da Forja, um App. As permissões abaixo são as da issue #4 e do
PRD; nenhuma a mais.

| Permissão (API) | Nível | Para quê |
|---|---|---|
| `contents` | read & write | clone do repositório, push na branch própria do run, criar e remover `refs/forja/claims/<n>` |
| `pull_requests` | write | abrir e atualizar o PR do run |
| `statuses` | write | publicar `forja/gates` e `forja/qa` como commit statuses |
| `issues` | read | ler título e corpo da issue que `forja.plan` e `forja.implement` entregam ao agente |
| `metadata` | read | obrigatória em todo App; lista o repositório e seus metadados |

O que o App **não** tem, de propósito:

- **Bypass de proteção de branch.** O App não entra em `bypass_actors` de
  nenhuma ruleset. A `main` continua protegida para ele exatamente como para
  qualquer colaborador sem bypass.
- `administration`, `workflows`, `checks`, `members`, `secrets`, `actions`,
  nem escrita em `issues`. Nada disso é necessário para o caso de uso
  "mantenedor liga a fábrica no seu repo". Em particular, sem `workflows` o App não consegue
  alterar `.github/workflows/**` — coerente com `human_merge_paths:
  [.github/**]` no contrato.
- Webhooks. A Forja é chamada pelo FuseOne via MCP, não por eventos do GitHub;
  o manifesto nasce com `hook_attributes.active: false`.

## Manifesto para registrar o App

Cole o JSON abaixo no fluxo de registro por manifesto da organização
(`https://github.com/organizations/fuseone-io/settings/apps/new`, ou o
formulário equivalente de "Register a GitHub App from a manifest"). O campo
`url` e `redirect_url` são informativos; a Forja não usa OAuth de usuário.

```json
{
  "name": "Forja (fuseone-io)",
  "url": "https://github.com/fuseone-io/fuseone-forja",
  "description": "Runtime de execucao da fabrica de software FuseOne. Abre PRs com evidencia verificavel; sem bypass de protecao de branch.",
  "public": false,
  "default_permissions": {
    "contents": "write",
    "pull_requests": "write",
    "statuses": "write",
    "issues": "read",
    "metadata": "read"
  },
  "default_events": [],
  "hook_attributes": {
    "active": false
  },
  "redirect_url": "https://github.com/fuseone-io/fuseone-forja",
  "request_oauth_on_install": false,
  "setup_on_update": false
}
```

Observações sobre campos do formulário que o manifesto não cobre:

- **Where can this GitHub App be installed?** → "Only on this account"
  (`public: false` acima). A Forja de uma instalação do FuseOne é um App por
  instalação, não um App de marketplace.
- **Private key:** gere **uma** chave no registro e leve o `.pem` direto para o
  cofre. Não deixe o arquivo baixado no laptop; não o cole em variável de
  ambiente, em issue nem em chat.
- Anote o **App ID** (visível na página do App) e, após instalar, o
  **Installation ID** (último segmento da URL
  `https://github.com/organizations/fuseone-io/settings/installations/<id>`).

## Escopo da instalação

**Apenas o repositório piloto.** Na tela de instalação, escolher "Only select
repositories" e marcar só `fuseone-io/fuseone-forja` (ou o piloto real, quando
nomeado). Nunca "All repositories": a Forja só precisa alcançar o repositório
cujo contrato ela vai executar, e a lista de repositórios da instalação é a
fronteira que o token de instalação respeita.

Quando um segundo repositório entrar, ele é adicionado à **mesma** instalação,
um por um, pelo humano. O token de instalação que o plano de controle gera para
um run é ainda restringido ao repositório daquele run (`repositories: [<repo>]`
no POST de `access_tokens`), de modo que um run nunca vê um token válido para
outro repositório da instalação.

## Como o plano de controle lê as credenciais

Nada disso chega ao workspace (regra 4 do `CLAUDE.md`). O workspace faz
checkout, build e gates sobre uma árvore já clonada; push, PR e statuses
acontecem no plano de controle com o token do App.

| Variável | Conteúdo | Obrigatória em produção |
|---|---|---|
| `FORJA_GITHUB_APP_ID` | App ID numérico | sim |
| `FORJA_GITHUB_INSTALLATION_ID` | Installation ID numérico da instalação no piloto | sim |
| `FORJA_GITHUB_PRIVATE_KEY_FILE` | **caminho** de um arquivo PEM montado pelo cofre (secret do Kubernetes, volume do Vault, etc.), legível só pelo processo `forjad` | sim |
| `FORJA_GITHUB_TOKEN` | fine-grained PAT **restrito ao piloto**, com as mesmas cinco permissões acima | **nunca em produção**; só desenvolvimento e testes |

Regras:

1. A chave privada **nunca** vai em variável de ambiente, nem em base64. Só o
   caminho. Variáveis vazam em `ps`, em dumps de ambiente e em logs de
   processo; um arquivo com modo `0400` montado pelo cofre não.
2. O plano de controle troca a chave por um **JWT de App** (RS256, validade de
   até 10 min) e o JWT por um **token de instalação** (validade de 1 h) com
   `POST /app/installations/{installation_id}/access_tokens`, restringindo
   `repositories` ao repositório do run e `permissions` ao subconjunto que o
   passo precisa (ex.: só `statuses: write` para publicar `forja/gates`).
3. O token de instalação vive no processo do plano de controle, pelo tempo de
   um run, e é a credencial que `clone`, `push`, `pulls` e `statuses` usam.
   Ele é citado nos logs apenas por referência (run id), nunca pelo valor.
4. `FORJA_GITHUB_TOKEN` existe para rodar `forjad` no laptop e nos testes de
   integração sem um App. Ele entra pela **mesma interface** que o App
   satisfaz, para que nenhum código fora do pacote `github` saiba qual dos dois
   está por baixo. Quando `FORJA_GITHUB_APP_ID` está definido, `FORJA_GITHUB_TOKEN`
   é ignorado e um aviso é registrado; quando nenhum dos dois está definido,
   `forjad doctor` falha com erro explícito, não com um `401` adiante.

A interface, declarada por quem consome o token (pacote `internal/github`,
ainda a ser criado pela fatia que fizer o primeiro clone):

```go
// TokenSource yields a GitHub token scoped to one repository for one run.
// The App implementation mints an installation token; the development
// implementation returns FORJA_GITHUB_TOKEN unchanged.
type TokenSource interface {
    Token(ctx context.Context, repo string) (Token, error)
}

type Token struct {
    Value     string
    ExpiresAt time.Time
}
```

Duas implementações: `AppTokenSource` (App ID + Installation ID + PEM lido do
caminho) e `StaticTokenSource` (o PAT). O teste de contrato roda contra as
duas; a de App usa um servidor HTTP falso que imita a forma da resposta de
`access_tokens` (campos `token`, `expires_at`, `permissions`, `repositories`).

## Ruleset que a `main` do piloto precisa

Hoje a `main` do piloto **não está protegida** (medições abaixo). A ruleset que
o critério de aceite da issue #4 exige — "push direto na `main` pelo App
continua bloqueado" — é esta, aplicada à branch default:

| Regra | Valor | Por quê |
|---|---|---|
| `deletion` | — | ninguém apaga a `main` |
| `non_fast_forward` | — | ninguém reescreve a `main` |
| `pull_request` | `required_approving_review_count: 0`, `dismiss_stale_reviews_on_push: true`, `require_last_push_approval: false` | toda mudança entra por PR; sem push direto, nem pelo App. Zero aprovações obrigatórias porque o merge já é serial e humano (ou o merger do workflow com o token do humano); o humano decide se sobe para 1 ao ratificar |
| `required_status_checks` | contexto `check` (integração `15368`, GitHub Actions), `strict_required_status_checks_policy: true` | `check` é o único check que existe na `main` hoje (medido); `forja/gates` e `forja/qa` entram aqui quando a Forja passar a publicá-los |
| `bypass_actors` | **vazio** | o App não tem bypass; a conta humana também não precisa, porque merge por PR com `check` verde é o único caminho |

Já existe uma ruleset chamada `default` (id `26307933`) no repositório, com
`enforcement: disabled`, só `deletion` e `non_fast_forward`, e um
`bypass_actors` para o usuário `phbpx` em modo `always`. O caminho mais curto é
**atualizá-la** (PUT) em vez de criar uma segunda. Corpo para
`gh api -X PUT repos/fuseone-io/fuseone-forja/rulesets/26307933 --input ruleset.json`,
rodado pelo humano (é escrita):

```json
{
  "name": "default",
  "target": "branch",
  "enforcement": "active",
  "bypass_actors": [],
  "conditions": {
    "ref_name": { "include": ["~DEFAULT_BRANCH"], "exclude": [] }
  },
  "rules": [
    { "type": "deletion" },
    { "type": "non_fast_forward" },
    {
      "type": "pull_request",
      "parameters": {
        "required_approving_review_count": 0,
        "dismiss_stale_reviews_on_push": true,
        "require_code_owner_review": false,
        "require_last_push_approval": false,
        "required_review_thread_resolution": false
      }
    },
    {
      "type": "required_status_checks",
      "parameters": {
        "strict_required_status_checks_policy": true,
        "required_status_checks": [
          { "context": "check", "integration_id": 15368 }
        ]
      }
    }
  ]
}
```

Se o humano preferir manter o próprio bypass para emergências, a forma menos
permissiva é `bypass_mode: "pull_request"` (bypass só via PR, nunca push
direto) — e o App continua fora da lista em qualquer caso.

## Verificação depois de instalar

Tudo abaixo é leitura ou uma escrita que **deve falhar**. `$TOKEN` é um token
de instalação do App, gerado pelo plano de controle (ou, enquanto `forjad`
não o faz, com qualquer ferramenta que assine o JWT RS256 e chame
`access_tokens`). Nenhum destes comandos usa a chave privada diretamente.

1. A ruleset está ativa na `main` e tem as quatro regras:

   ```sh
   gh api repos/fuseone-io/fuseone-forja/rules/branches/main \
     --jq '[.[] | .type] | sort'
   # esperado: ["deletion","non_fast_forward","pull_request","required_status_checks"]
   ```

2. O App está instalado **só** no piloto (requer owner da organização):

   ```sh
   gh api orgs/fuseone-io/installations \
     --jq '.installations[] | select(.app_slug=="forja-fuseone-io") | {id, repository_selection, permissions}'
   # esperado: repository_selection == "selected"; permissions == {contents:write, pull_requests:write, statuses:write, issues:read, metadata:read}

   curl -sS -H "Authorization: Bearer $TOKEN" \
     https://api.github.com/installation/repositories \
     | jq '{total_count, repos: [.repositories[].full_name]}'
   # esperado: {"total_count":1,"repos":["fuseone-io/fuseone-forja"]}
   ```

3. O token do App **não** empurra na `main` (a escrita tem de ser recusada):

   ```sh
   /usr/bin/git -C /caminho/clone-descartavel \
     -c "http.https://github.com/.extraheader=AUTHORIZATION: bearer $TOKEN" \
     push https://github.com/fuseone-io/fuseone-forja.git HEAD:refs/heads/main
   # esperado: exit 1, com "GH013: Repository rule violations found for refs/heads/main"
   # e "Changes must be made through a pull request."
   ```

4. O mesmo token **consegue** empurrar numa branch de run e criar a ref de
   reserva (prova de que `contents: write` está lá):

   ```sh
   /usr/bin/git -C /caminho/clone-descartavel \
     -c "http.https://github.com/.extraheader=AUTHORIZATION: bearer $TOKEN" \
     push https://github.com/fuseone-io/fuseone-forja.git HEAD:refs/heads/forja/smoke-4
   # esperado: exit 0

   /usr/bin/git -C /caminho/clone-descartavel \
     -c "http.https://github.com/.extraheader=AUTHORIZATION: bearer $TOKEN" \
     push https://github.com/fuseone-io/fuseone-forja.git :refs/heads/forja/smoke-4
   # esperado: exit 0 (limpa a branch de teste)
   ```

5. O token do App **não** tem bypass (deve ser `"never"`; `"always"` ou
   `"pull_requests_only"` significa que o App entrou em `bypass_actors`):

   ```sh
   curl -sS -H "Authorization: Bearer $TOKEN" \
     https://api.github.com/repos/fuseone-io/fuseone-forja/rulesets/26307933 \
     | jq '.current_user_can_bypass'
   # esperado: "never"
   ```

Os resultados destes cinco comandos, colados num comentário da issue #4, são a
evidência que fecha os critérios "sem bypass" e "push direto bloqueado".

## Medições feitas nesta sessão (somente leitura)

Ambiente: `gh` autenticado como `phbpx` (OAuth token com escopos `admin:org,
admin:public_key, delete_repo, gist, project, repo`), organização
`fuseone-io`, repositório `fuseone-io/fuseone-forja`, `origin/main` em
`ce95147`. Todos os comandos abaixo foram rodados em 2026-11-01.

| Afirmação | Resultado | Comando |
|---|---|---|
| O piloto é público, na organização `fuseone-io`, branch default `main` | **confirmado** | `gh api repos/fuseone-io/fuseone-forja --jq '{full_name,private,visibility,default_branch,owner_type:.owner.type}'` → `{"default_branch":"main","full_name":"fuseone-io/fuseone-forja","owner_type":"Organization","private":false,"visibility":"public"}` |
| A organização está no plano `team` | **confirmado** | `gh api orgs/fuseone-io --jq '{login,plan:.plan.name,type}'` → `{"login":"fuseone-io","plan":"team","type":"Organization"}` |
| A `main` do piloto tem proteção de branch clássica | **refutado** — não tem | `gh api repos/fuseone-io/fuseone-forja/branches/main/protection` → HTTP 404 `{"message":"Branch not protected"}`; `gh api repos/fuseone-io/fuseone-forja/branches/main --jq '{protected,protection}'` → `{"protected":false,"protection":{"enabled":false,"required_status_checks":{"checks":[],"contexts":[],"enforcement_level":"off"}}}` |
| Alguma regra se aplica à `main` hoje | **refutado** — nenhuma | `gh api repos/fuseone-io/fuseone-forja/rules/branches/main` → `[]` |
| Rulesets no repositório | **1 de 1**, desativada | `gh api repos/fuseone-io/fuseone-forja/rulesets` → 1 item: `{"id":26307933,"name":"default","target":"branch","enforcement":"disabled"}`; `gh api repos/fuseone-io/fuseone-forja/rulesets/26307933` → `rules: [deletion, non_fast_forward]`, `conditions.ref_name.include: ["~DEFAULT_BRANCH"]`, `bypass_actors: [{actor_id: 3296831, actor_type: User, bypass_mode: always}]`, `current_user_can_bypass: always` |
| O ator de bypass `3296831` é `phbpx` | **confirmado** | `gh api user/3296831 --jq '{login,type}'` → `{"login":"phbpx","type":"User"}` |
| Rulesets no nível da organização | **não medido** | `gh api orgs/fuseone-io/rulesets` → HTTP 404 `{"message":"Not Found"}` (o token é de um membro, não de owner) |
| Apps instalados na organização | **não medido** | `gh api orgs/fuseone-io/installations` → HTTP 404 `{"message":"Not Found"}`; `gh api user/installations` → HTTP 403 `{"message":"You must authenticate with an access token authorized to a GitHub App in order to list installations"}` |
| Quem é owner da organização | **confirmado**: `klinux`, `sergiomonteiro`; `phbpx` é **member** | `gh api 'orgs/fuseone-io/members?role=admin' --jq '[.[] | .login]'` → `["klinux","sergiomonteiro"]`; `gh api orgs/fuseone-io/memberships/phbpx --jq '{role,state}'` → `{"role":"member","state":"active"}` |
| Admins do repositório | **confirmado**: 3 de 3 colaboradores são `admin` | `gh api repos/fuseone-io/fuseone-forja/collaborators --jq '[.[] | {login, role_name}]'` → `klinux`, `sergiomonteiro`, `phbpx`, todos `admin` |
| Existe um check chamado `check` na `main`, para a ruleset exigir | **confirmado** | `gh api repos/fuseone-io/fuseone-forja/commits/main/check-runs --jq '[.check_runs[] \| {name, app_id: .app.id, app_slug: .app.slug}]'` → `[{"app_id":15368,"app_slug":"github-actions","name":"check"}]` (1 check run de 1) |
| Commit statuses na `main` (onde `forja/gates` e `forja/qa` vão aparecer) | **0 de 0** hoje | `gh api repos/fuseone-io/fuseone-forja/commits/main/status --jq '{state,total_count}'` → `{"state":"pending","total_count":0}` |
| Membros podem registrar GitHub Apps na organização | **não medido** | não há endpoint REST de leitura para essa configuração; só a UI de owner (`Settings → Member privileges → GitHub Apps`) |

### Achados para o humano

1. **A `main` do piloto está aberta hoje.** Nenhuma regra ativa, nenhuma
   proteção clássica; a única ruleset está desativada e, mesmo ativa, não
   exigiria PR nem check. O critério "push direto na `main` pelo App continua
   bloqueado pela ruleset" é falso até a ruleset acima ser aplicada — para o
   App e para qualquer colaborador.
2. **Quem está operando (`phbpx`) é member, não owner, da organização.**
   Registrar um App no nível da organização é ação de owner, a menos que a
   organização permita a membros (não medido). Ou um owner (`klinux` ou
   `sergiomonteiro`) registra o App, ou concede owner a quem vai operar a
   Forja, ou habilita a permissão para membros.
3. O check `check` existe e é publicado pela integração `15368` (GitHub
   Actions); é o contexto que a ruleset deve exigir hoje.

## O que só o humano pode fazer

- [ ] Um **owner** da organização (`klinux` ou `sergiomonteiro`) registra o App
      pelo manifesto acima em `fuseone-io`, ou concede owner a `phbpx`, ou
      habilita "members can create GitHub Apps" — e diz qual das três escolheu.
- [ ] Gerar **uma** chave privada no registro e gravá-la no cofre que o plano de
      controle lê; o PEM é montado como arquivo no caminho de
      `FORJA_GITHUB_PRIVATE_KEY_FILE`. Nenhuma cópia fora do cofre.
- [ ] Instalar o App **só** em `fuseone-io/fuseone-forja` ("Only select
      repositories"). Anotar App ID e Installation ID no cofre, ao lado da chave.
- [ ] Aplicar a ruleset acima na `main` do piloto (PUT na ruleset `26307933`),
      com `enforcement: active`, `bypass_actors: []` e o check `check`
      obrigatório. Decidir se `required_approving_review_count` fica em 0 ou
      sobe para 1.
- [ ] Rodar os cinco comandos de "Verificação depois de instalar" e colar as
      saídas (com os exit codes) num comentário da issue #4. Só então os
      critérios "sem bypass" e "push direto bloqueado" estão **verificados**.
- [ ] Ratificar o modelo de credenciais (`FORJA_GITHUB_APP_ID`,
      `FORJA_GITHUB_INSTALLATION_ID`, `FORJA_GITHUB_PRIVATE_KEY_FILE`;
      `FORJA_GITHUB_TOKEN` só em desenvolvimento) antes da fatia que cria
      `internal/github`.
- [ ] Se o piloto real (outra área, ADR 0027) for nomeado antes disso tudo,
      apontar instalação e ruleset para ele em vez de `fuseone-forja`.

## Referências

- `docs/PRD.pdf` — caso de uso "Mantenedor liga a fábrica no seu repo".
- `CLAUDE.md` — regras 3 (evidência escrita pelo plano de controle), 4
  (nenhuma credencial entra no workspace) e 7 (nenhum segredo no repositório).
- [ADR 0027](adr/0027-repositorio-piloto.md) — repositório piloto (default
  provisório).
- Issue #4; issue #1 (piloto).
