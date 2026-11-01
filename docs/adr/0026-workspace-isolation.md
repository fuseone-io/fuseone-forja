# ADR 0026: workspace isolation and where Forja runs

- **Status:** proposed (default taken by the team; awaits human ratification)
- **Issue:** #3
- **Date:** 2026-11-01

## Context

Forja runs a repository's agent, build and gates inside an ephemeral
workspace. The PRD leaves three questions open for this decision:

1. **Workspace runtime:** container with gVisor (`runsc`) or Firecracker
   microVM.
2. **Placement:** the same cluster as FuseOne or a dedicated node pool.
3. **Egress:** how to deny and log, per run, any network exit that does not
   go through the model and registry proxies.

The decision fixes the cost of support services inside the run (Postgres as a
sidecar) and conditions the non-negotiable rules "no credential enters the
workspace" and "evidence is written by the control plane" of `CLAUDE.md`.

The PRD assigns this decision to a human. The human asked the team to proceed
without interruptions; this ADR records the **default** taken, the
measurements that could be made in read-only mode and what remains to be
measured. It is born as `proposed` and only becomes `accepted` with human
ratification (checklist at the end).

## Decision (default taken)

### Runtime

- **Now:** Docker with `runc`, behind the `Workspace` interface declared next
  to the gates runner (`internal/gates`). Nothing outside `internal/workspace`
  knows about Docker.
- **Hardening target:** gVisor (`runsc`) as the Docker runtime
  (`--runtime=runsc`). It keeps the same driver, the same image and the same
  sidecar model; the switch is a field in the container creation, not a
  second driver.
- **Firecracker:** deferred. It would require a second driver (rootfs, kernel,
  agent inside the VM, networking via tap) and a second sidecar model. It
  comes back to the table if **one** of the two conditions below occurs:
  1. a kernel-level escape is found in `runsc` that affects the syscall
     profile of the pilot's gates; or
  2. the overhead of `runsc` over `runc` on the pilot's gates measures
     **> 2x** in wall-clock time.

### Placement

**Dedicated node pool.** Per-run egress denial requires each workspace to live
in a network that the FuseOne cluster does not share; a dedicated pool also
isolates the blast radius of a compromised workspace and allows installing
`runsc` on the node without touching FuseOne's nodes.

### Egress

- Each run gets **its own Docker network created with `--internal`**. The
  model and registry proxies are connected to that network (a container can
  belong to more than one network; the proxy sits on the run's internal
  network and on the network with egress).
- The workspace **does not resolve external names**: Docker's embedded DNS
  answers `SERVFAIL` for names outside the network (measured below) and
  resolves only the run's siblings (sidecars and proxies).
- Refused connections are **logged per run by the control plane**, from the
  proxies' logs (what was requested and denied) and from the node's network
  logs. The workspace has no access to those logs.

## Measurements

### Environment

All measurements below were made in this session, on local Docker, with the
client on macOS talking to an Ubuntu VM (colima). It is not the environment
where Forja will run; the numbers serve for order of magnitude and for the
`runc` vs `runsc` comparison once the latter is installed.

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

### Table

| Measurement | Docker/runc (measured) | gVisor/runsc | Firecracker |
|---|---|---|---|
| Boot of the `golang:1.25` workspace, cold (includes pull of 1,27 GB) | 13 942 / 14 740 / 14 910 ms (3 runs) | not measured | not measured |
| Boot of the `golang:1.25` workspace, warm (local image) | 134 / 136 / 148 ms (3 runs) | not measured | not measured |
| `postgres:16` sidecar until `pg_isready` over TCP, cold (includes pull of 663 MB) | 8 984 ms (1 sample) | not measured | not measured |
| `postgres:16` sidecar until `pg_isready` over TCP, warm | 600 / 617 / 648 ms (3 runs) | not measured | not measured |
| Workspace on `--internal` network reaches sibling | yes (`pg_isready` exit 0, `ping` 0% loss) | not measured | not measured |
| Workspace on `--internal` network reaches `example.com` | no (`wget` exit 1, DNS `SERVFAIL`) | not measured | not measured |
| Workspace on `--internal` network reaches external IP without DNS | no (`ping 1.1.1.1` "Network unreachable"; `nc 93.184.215.14 80` exit 1) | not measured | not measured |
| Proxy on `--internal` + `bridge` network reaches `example.com` | yes (`wget` exit 0) | not measured | not measured |

"Boot" is the time between the `docker run` being issued by the client and
the container's process finishing (`go version`), measured with
`perl -MTime::HiRes` around the command. In cold mode the image was removed
with `docker rmi` before each run.

### Why the gVisor and Firecracker columns are empty

Neither `runsc` nor `firecracker` exist on this machine, nor in the daemon:

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

Installing a runtime on the node is a write outside what this turn can do; it
goes to the human checklist.

### Commands and outputs — workspace boot

Script: `measure-boot.sh <cold|warm> <n>` (removes the image in `cold` mode,
then times `docker run --rm golang:1.25 go version`).

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

### Commands and outputs — Postgres sidecar

Script: `measure-pg.sh <n>`: `docker run -d --rm --name forja-pg-<n>
-e POSTGRES_PASSWORD=forja postgres:16`, then `docker exec forja-pg-<n>
pg_isready -h 127.0.0.1 -U postgres` every 100 ms until exit 0. The target is
TCP (`-h 127.0.0.1`) on purpose: the temporary server of `initdb` listens only
on the Unix socket and does not count as "ready".

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

`container_start_ms` is the time until `docker run -d` returns;
`ready_total_ms` includes `initdb` and the server coming up on TCP.

### Commands and outputs — egress on an `--internal` network

Script: `measure-egress2.sh`. Creates the network, starts `postgres:16` as a
sibling, tests from a "workspace" container on the same network, and removes
everything at the end.

```
$ docker network create --internal forja-egress-test
$ docker network inspect forja-egress-test --format '{{.Name}} internal={{.Internal}} driver={{.Driver}} subnet={{(index .IPAM.Config 0).Subnet}}'
forja-egress-test internal=true driver=bridge subnet=172.20.0.0/16

$ docker run -d --rm --name forja-sib --network forja-egress-test -e POSTGRES_PASSWORD=forja postgres:16

=== 1a. workspace -> sibling postgres ===
$ docker run --rm --network forja-egress-test postgres:16 pg_isready -h forja-sib -U postgres
forja-sib:5432 - accepting connections          (exit 0, on the 3rd attempt)

=== 1b. workspace -> sibling (ICMP by name) ===
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

=== 4. workspace -> 1.1.1.1 (ICMP, no DNS) ===
$ docker run --rm --network forja-egress-test alpine:3.20 ping -c 2 -W 2 1.1.1.1
ping: sendto: Network unreachable               (exit 1)

=== 5. workspace -> 93.184.215.14:80 (TCP by IP, no DNS) ===
$ docker run --rm --network forja-egress-test alpine:3.20 nc -z -w 5 93.184.215.14 80
(exit 1)

$ docker stop forja-sib
$ docker network rm forja-egress-test
```

The proxy model was verified in an earlier execution of the same scenario
(`measure-egress.sh`): a container on the `--internal` network **and** on the
`bridge` reaches `example.com`.

```
$ docker run -d --rm --name forja-proxy --network forja-egress-test alpine:3.20 sleep 300
$ docker network connect bridge forja-proxy
$ docker exec forja-proxy wget -T 5 -qO- http://example.com
<!doctype html><html lang=en><head>...<title>Example Domain</title>...   (exit 0)
```

In that earlier execution the sibling was a `busybox httpd` that exited right
after coming up; the sibling test was redone with `postgres:16` above.

## Consequences

- The cost of a support Postgres per run, with the image already on the node,
  is on the order of **0,6 s** until it accepts TCP connections; the dominant
  cost when cold is the pull (**~9 s** for 663 MB on this network). An image
  cache on the dedicated pool (pre-pull of the contract's images) takes the
  cold path off the critical path.
- Booting the `golang:1.25` workspace with a local image takes **~0,14 s**;
  cold it takes **~14 s** and it is all pull (1,27 GB). Same conclusion:
  pre-pull on the pool.
- One `--internal` network per run denies egress by IP and by name with no
  additional iptables in the control plane; what the workspace reaches is
  exactly the set of containers connected to that network. The proxy
  connected to two networks is the only bridge, and it is the one that logs
  what was requested.
- Swapping `runc` for `runsc` changes neither the `Workspace` interface nor
  the network model above; it changes one container-creation parameter and
  one package on the node.
- While `runsc` is not installed, isolation is that of `runc` with Docker's
  default seccomp and AppArmor — insufficient for third-party code in
  production. Hardening is its own slice, after this ADR.

## What only the human can do

- [ ] Ratify runtime (Docker/runc now, gVisor as target), placement
      (dedicated pool) and egress (`--internal` network per run + proxies) —
      or choose differently. On ratification, change this ADR's status to
      `accepted`.
- [ ] Provision a node (or VM) with `runsc` installed and registered in
      Docker (`docker info` listing `runsc` under `Runtimes`) to fill the
      gVisor column of this table with the same scripts.
- [ ] Decide whether the Firecracker column deserves to be measured before
      phase 1, or whether the two revisit conditions above suffice.
- [ ] Confirm that the FuseOne cluster's network (CNI, policies) imposes no
      restrictions on the dedicated pool running Docker with `--internal`
      networks per run.
- [ ] Run the pilot repository's gates on `runc` and on `runsc` and record
      the overhead (revisit criterion: > 2x).

## References

- `docs/PRD.pdf` — Forja: execution runtime of the software factory.
- `CLAUDE.md` — rules 3 (evidence by the control plane) and 4 (no credential
  in the workspace).
- Issue #3.
