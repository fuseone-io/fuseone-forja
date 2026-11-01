# ADR 0026: isolamento do workspace e onde a Forja roda

- **Status:** proposto (padrão tomado pelo time; aguarda ratificação humana)
- **Issue:** #3
- **Data:** 2026-11-01

## Contexto

A Forja executa o agente, o build e os gates de um repositório dentro de um
workspace efêmero. O PRD deixa três perguntas em aberto para esta decisão:

1. **Runtime do workspace:** container com gVisor (`runsc`) ou microVM
   Firecracker.
2. **Placement:** o mesmo cluster do FuseOne ou um pool de nós dedicado.
3. **Egress:** como negar e registrar, por run, qualquer saída de rede que não
   passe pelos proxies de modelo e de registry.

A decisão fixa o custo de serviços de apoio dentro do run (Postgres como
sidecar) e condiciona as regras não negociáveis "nenhuma credencial entra no
workspace" e "a evidência é escrita pelo plano de controle" do `CLAUDE.md`.

O PRD aponta esta decisão para um humano. O humano pediu que o time seguisse
sem interrupções; este ADR registra o **padrão** tomado, as medições que
puderam ser feitas em modo somente leitura e o que ficou por medir. Ele nasce
como `proposto` e só vira `aceito` com a ratificação humana (checklist ao fim).

## Decisão (padrão tomado)

### Runtime

- **Agora:** Docker com `runc`, atrás da interface `Workspace` declarada ao lado
  do runner de gates (`internal/gates`). Nada fora de `internal/workspace`
  conhece o Docker.
- **Alvo de endurecimento:** gVisor (`runsc`) como runtime do Docker
  (`--runtime=runsc`). Ele mantém o mesmo driver, a mesma imagem e o mesmo
  modelo de sidecar; a troca é um campo na criação do container, não um
  segundo driver.
- **Firecracker:** adiado. Exigiria um segundo driver (rootfs, kernel, agente
  dentro da VM, rede por tap) e um segundo modelo de sidecar. Volta à mesa se
  ocorrer **uma** das duas condições abaixo:
  1. uma fuga em nível de kernel for encontrada em `runsc` que afete o perfil
     de syscalls dos gates do piloto; ou
  2. o overhead de `runsc` sobre `runc` nos gates do piloto medir **> 2x** no
     tempo de parede.

### Placement

**Pool de nós dedicado.** A negação de egress por run exige que cada workspace
viva numa rede que o cluster do FuseOne não compartilhe; um pool dedicado
também isola o blast radius de um workspace comprometido e permite instalar
`runsc` no node sem tocar nos nós do FuseOne.

### Egress

- Cada run recebe **uma rede Docker própria criada com `--internal`**. Os
  proxies de modelo e de registry são conectados a essa rede (um container
  pode pertencer a mais de uma rede; o proxy fica na rede interna do run e na
  rede com saída).
- O workspace **não resolve nomes externos**: o DNS embutido do Docker
  responde `SERVFAIL` para nomes fora da rede (medido abaixo) e resolve só os
  siblings do run (sidecars e proxies).
- Conexões recusadas são **registradas por run pelo plano de controle**, a
  partir dos logs dos proxies (o que foi pedido e negado) e dos logs de rede
  do node. O workspace não tem acesso a esses logs.

## Medições

### Ambiente

Todas as medições abaixo foram feitas nesta sessão, no Docker local, com o
cliente no macOS falando com uma VM Ubuntu (colima). Não é o ambiente onde a
Forja vai rodar; os números servem para ordem de grandeza e para a comparação
`runc` vs `runsc` quando o segundo for instalado.

```
$ docker version
Client: Docker Engine - Community
 Version:           29.9.0
 OS/Arch:           darwin/arm64
 Context:           colima
Server: Docker Engine - Community
 Engine:
  Version:          29.5.2
  OS/Arch:          linux/arm64
 containerd:        v2.2.4
 runc:              1.3.5

$ docker info
 Storage Driver: overlayfs (driver-type: io.containerd.snapshotter.v1)
 Cgroup Version: 2
 Runtimes: io.containerd.runc.v2 runc
 Default Runtime: runc
 Kernel Version: 6.8.0-117-generic
 Operating System: Ubuntu 24.04.4 LTS
 Architecture: aarch64
 CPUs: 4
 Total Memory: 7.738GiB
```

### Tabela

| Medição | Docker/runc (medido) | gVisor/runsc | Firecracker |
|---|---|---|---|
| Boot do workspace `golang:1.25`, frio (inclui pull de 1,27 GB) | 13 942 / 14 740 / 14 910 ms (3 runs) | não medido | não medido |
| Boot do workspace `golang:1.25`, quente (imagem local) | 134 / 136 / 148 ms (3 runs) | não medido | não medido |
| `postgres:16` sidecar até `pg_isready` em TCP, frio (inclui pull de 663 MB) | 8 984 ms (1 amostra) | não medido | não medido |
| `postgres:16` sidecar até `pg_isready` em TCP, quente | 600 / 617 / 648 ms (3 runs) | não medido | não medido |
| Workspace em rede `--internal` alcança sibling | sim (`pg_isready` exit 0, `ping` 0% perda) | não medido | não medido |
| Workspace em rede `--internal` alcança `example.com` | não (`wget` exit 1, DNS `SERVFAIL`) | não medido | não medido |
| Workspace em rede `--internal` alcança IP externo sem DNS | não (`ping 1.1.1.1` "Network unreachable"; `nc 93.184.215.14 80` exit 1) | não medido | não medido |
| Proxy em rede `--internal` + `bridge` alcança `example.com` | sim (`wget` exit 0) | não medido | não medido |

"Boot" é o tempo entre o `docker run` ser emitido pelo cliente e o processo do
container terminar (`go version`), medido com `perl -MTime::HiRes` em volta do
comando. Em modo frio a imagem foi removida com `docker rmi` antes de cada run.

### Por que as colunas gVisor e Firecracker estão vazias

Nem `runsc` nem `firecracker` existem nesta máquina, nem no daemon:

```
$ which runsc
runsc not found
(exit 1)

$ docker run --rm --runtime=runsc alpine:3.20 true
docker: Error response from daemon: unknown or invalid runtime name: runsc
(exit 125)

$ which firecracker
firecracker not found
(exit 1)
```

Instalar um runtime no node é escrita fora do que este turno pode fazer; fica
no checklist humano.

### Comandos e saídas — boot do workspace

Script: `measure-boot.sh <cold|warm> <n>` (remove a imagem em modo `cold`,
depois cronometra `docker run --rm golang:1.25 go version`).

```
$ bash measure-boot.sh cold 1
+ docker rmi golang:1.25
Error response from daemon: No such image: golang:1.25
+ docker run --rm golang:1.25 go version
Unable to find image 'golang:1.25' locally
1.25: Pulling from library/golang
...
Status: Downloaded newer image for golang:1.25
go version go1.25.14 linux/arm64
mode=cold run=1 exit=0 elapsed_ms=14910

$ bash measure-boot.sh cold 2
+ docker rmi golang:1.25
Untagged: golang:1.25
Deleted: sha256:699337d620559a59b4a2bb298ad59611e535d2ee755a34cf2d2a98f37578dc80
+ docker run --rm golang:1.25 go version
...
mode=cold run=2 exit=0 elapsed_ms=14740

$ bash measure-boot.sh cold 3
...
mode=cold run=3 exit=0 elapsed_ms=13942

$ bash measure-boot.sh warm 1
+ docker images -q golang:1.25
699337d62055
+ docker run --rm golang:1.25 go version
go version go1.25.14 linux/arm64
mode=warm run=1 exit=0 elapsed_ms=134

$ bash measure-boot.sh warm 2
mode=warm run=2 exit=0 elapsed_ms=136

$ bash measure-boot.sh warm 3
mode=warm run=3 exit=0 elapsed_ms=148

$ docker images --format '{{.Repository}}:{{.Tag}} {{.Size}}' golang
golang:1.25 1.27GB
```

### Comandos e saídas — Postgres sidecar

Script: `measure-pg.sh <n>`: `docker run -d --rm --name forja-pg-<n>
-e POSTGRES_PASSWORD=forja postgres:16`, depois `docker exec forja-pg-<n>
pg_isready -h 127.0.0.1 -U postgres` a cada 100 ms até exit 0. O alvo é TCP
(`-h 127.0.0.1`) de propósito: o servidor temporário do `initdb` escuta só no
socket Unix e não conta como "pronto".

```
$ bash measure-pg.sh 1
+ docker images -q postgres:16
+ docker run -d --rm --name forja-pg-1 -e POSTGRES_PASSWORD=forja postgres:16
Unable to find image 'postgres:16' locally
16: Pulling from library/postgres
...
Status: Downloaded newer image for postgres:16
+ docker exec forja-pg-1 pg_isready -h 127.0.0.1 -U postgres
127.0.0.1:5432 - accepting connections
run=1 pg_isready_exit=0 tries=4 container_start_ms=8269 ready_total_ms=8984

$ bash measure-pg.sh 2
run=2 pg_isready_exit=0 tries=3 container_start_ms=91 ready_total_ms=648

$ bash measure-pg.sh 3
run=3 pg_isready_exit=0 tries=3 container_start_ms=87 ready_total_ms=617

$ bash measure-pg.sh 4
run=4 pg_isready_exit=0 tries=3 container_start_ms=92 ready_total_ms=600

$ docker images --format '{{.Repository}}:{{.Tag}} {{.Size}}' postgres
postgres:16 663MB
```

`container_start_ms` é o tempo até o `docker run -d` retornar;
`ready_total_ms` inclui o `initdb` e a subida do servidor em TCP.

### Comandos e saídas — egress em rede `--internal`

Script: `measure-egress2.sh`. Cria a rede, sobe `postgres:16` como sibling,
testa de um container "workspace" na mesma rede, e remove tudo ao fim.

```
$ docker network create --internal forja-egress-test
$ docker network inspect forja-egress-test --format '{{.Name}} internal={{.Internal}} driver={{.Driver}} subnet={{(index .IPAM.Config 0).Subnet}}'
forja-egress-test internal=true driver=bridge subnet=172.20.0.0/16

$ docker run -d --rm --name forja-sib --network forja-egress-test -e POSTGRES_PASSWORD=forja postgres:16

=== 1a. workspace -> sibling postgres ===
$ docker run --rm --network forja-egress-test postgres:16 pg_isready -h forja-sib -U postgres
forja-sib:5432 - accepting connections          (exit 0, na 3a tentativa)

=== 1b. workspace -> sibling (ICMP por nome) ===
$ docker run --rm --network forja-egress-test alpine:3.20 ping -c 2 -W 2 forja-sib
PING forja-sib (172.20.0.2): 56 data bytes
2 packets transmitted, 2 packets received, 0% packet loss   (exit 0)

=== 2. workspace -> example.com (HTTP) ===
$ docker run --rm --network forja-egress-test alpine:3.20 wget -T 5 -O- http://example.com
wget: bad address 'example.com'                 (exit 1)

=== 3. workspace DNS example.com ===
$ docker run --rm --network forja-egress-test alpine:3.20 nslookup example.com
Server:		127.0.0.11
** server can't find example.com: SERVFAIL      (exit 1)

=== 4. workspace -> 1.1.1.1 (ICMP, sem DNS) ===
$ docker run --rm --network forja-egress-test alpine:3.20 ping -c 2 -W 2 1.1.1.1
ping: sendto: Network unreachable               (exit 1)

=== 5. workspace -> 93.184.215.14:80 (TCP por IP, sem DNS) ===
$ docker run --rm --network forja-egress-test alpine:3.20 nc -z -w 5 93.184.215.14 80
(exit 1)

$ docker stop forja-sib
$ docker network rm forja-egress-test
```

O modelo de proxy foi verificado numa execução anterior do mesmo cenário
(`measure-egress.sh`): um container na rede `--internal` **e** na `bridge`
alcança `example.com`.

```
$ docker run -d --rm --name forja-proxy --network forja-egress-test alpine:3.20 sleep 300
$ docker network connect bridge forja-proxy
$ docker exec forja-proxy wget -T 5 -qO- http://example.com
<!doctype html><html lang=en><head>...<title>Example Domain</title>...   (exit 0)
```

Nessa execução anterior o sibling era um `busybox httpd` que saiu logo após
subir; o teste do sibling foi refeito com `postgres:16` acima.

## Consequências

- O custo de um Postgres de apoio por run, com a imagem já no node, é da ordem
  de **0,6 s** até aceitar conexões em TCP; o custo dominante em frio é o pull
  (**~9 s** para 663 MB nesta rede). Um cache de imagens no pool dedicado
  (pré-pull das imagens do contrato) tira o frio do caminho crítico.
- O boot do workspace `golang:1.25` com imagem local é de **~0,14 s**; o frio
  é de **~14 s** e é todo pull (1,27 GB). Mesma conclusão: pré-pull no pool.
- Uma rede `--internal` por run nega saída por IP e por nome sem iptables
  adicionais no plano de controle; o que o workspace alcança é exatamente o
  conjunto de containers conectados àquela rede. O proxy conectado a duas
  redes é a única ponte, e é ele que registra o que foi pedido.
- Trocar `runc` por `runsc` não muda a interface `Workspace` nem o modelo de
  rede acima; muda um parâmetro de criação do container e um pacote no node.
- Enquanto `runsc` não estiver instalado, o isolamento é o de `runc` com
  seccomp e AppArmor padrão do Docker — insuficiente para código de terceiros
  em produção. O endurecimento é uma fatia própria, após este ADR.

## O que só o humano pode fazer

- [ ] Ratificar runtime (Docker/runc agora, gVisor como alvo), placement (pool
      dedicado) e egress (rede `--internal` por run + proxies) — ou escolher
      diferente. Ao ratificar, trocar o status deste ADR para `aceito`.
- [ ] Provisionar um node (ou VM) com `runsc` instalado e registrado no Docker
      (`docker info` listando `runsc` em `Runtimes`) para preencher a coluna
      gVisor desta tabela com os mesmos scripts.
- [ ] Decidir se a coluna Firecracker merece ser medida antes da fase 1, ou se
      as duas condições de revisita acima bastam.
- [ ] Confirmar que a rede do cluster do FuseOne (CNI, políticas) não impõe
      restrições ao pool dedicado rodar Docker com redes `--internal` por run.
- [ ] Rodar os gates do repositório piloto em `runc` e em `runsc` e registrar o
      overhead (critério de revisita: > 2x).

## Referências

- `docs/PRD.pdf` — Forja: runtime de execução da fábrica de software.
- `CLAUDE.md` — regras 3 (evidência pelo plano de controle) e 4 (nenhuma
  credencial no workspace).
- Issue #3.
