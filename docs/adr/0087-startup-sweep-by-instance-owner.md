# 0087 · Startup sweep of Docker objects by instance owner

Status: proposed

## Context

A run's containers and its internal network carry the label `forja.run`.
`Close` removes them. Two cases leave them behind:

- A create the daemon completes after its run gave up. The driver waits up
  to `createGrace` (30 s) for the answer so that `Close` can remove the
  object. When the daemon takes longer, the id is lost.
- A `forjad` that dies mid-run never reaches `Close`.

What stays is a container created and never started, or a running one, and
the run's network. They use resources. A sidecar's config holds the
contract's `services.env`, readable by anyone with access to the daemon. A
retry with the same run id fails on the name collision.

A sweep inside `Close` was rejected: it races the create in flight. At
startup no create of this process is in flight, so a sweep there is safe,
if it removes only this process's objects.

### Can two `forjad` share one daemon?

Yes. Nothing in the code or the deployment prevents it:

- The workspace driver builds its client from the environment
  (`client.FromEnv`: `DOCKER_HOST` and the variables next to it). Any number
  of processes can point at the same daemon.
- The state directory lock (`FORJA_STATE_DIR/forjad.lock`, ADR 0076) is per
  directory, not per daemon. ADR 0076 states that each replica needs its own
  `FORJA_STATE_DIR`, so several replicas are expected.
- The roadmap leaves open "the Docker host each replica drives". Nothing
  says it is one host per replica.
- The repository's own Docker tests already do it: several worktrees run
  `make docker-test` in parallel against one colima daemon.

A sweep by `forja.run` alone would remove another instance's live runs.

## Decision

1. **Owner label.** Every container and network the workspace creates also
   carries `forja.owner=<instance id>`. That covers the network, the
   gateway, the registry and model proxies, the workspace and every
   sidecar.
2. **Instance id.** The id is 16 random bytes in hex, made on the state
   directory's first start and stored in `FORJA_STATE_DIR/instance`. It is
   written to a temporary file and renamed into place. It is read from the
   directory, not derived from its path: two hosts can share one path and
   one daemon. The directory's lock makes the id one live process's alone.
   The id survives a crash, so the next start knows the dead process's
   objects as its own. A file that does not hold an id fails the start and
   names the file.
3. **Sweep.** `serve` sweeps after it locks the directory and before it
   opens the run store and recovers the claims. The sweep needs neither, so
   a database or GitHub that fails the start cannot keep the objects there.
   It removes every container, in any state, then every network, that
   carries both `forja.run` and this instance's `forja.owner`. Containers go
   first because the daemon refuses to remove a network that still has
   something attached.
4. **Never fatal.** A daemon that fails or does not answer is reported on
   stderr and the start goes on. The sweep is bounded by 30 s, inside the
   startup deadline. What it could not remove waits for the next start. A
   start that was already stopped does not sweep.
5. **The report.** One line with the number of containers and networks
   removed and the ids of their runs. Never the env, the command or the
   config: `workspace.Swept` holds only counts and run ids.
6. **Objects with no owner label are never swept.** That covers objects made
   before this change and those of an instance with no `FORJA_STATE_DIR`,
   which has no lasting id and labels no owner. Nothing tells whether a live
   process still owns them. The operator removes them by hand once no
   older `forjad`, and no `forjad` without a state directory, is running on
   the daemon. Docker cannot filter on a missing label, so the commands list
   every `forja.run` object with its owner and keep only those with none.
   Objects that carry `forja.owner` belong to an instance that may be live,
   and are left alone:

   ```sh
   docker ps -a --filter label=forja.run --format '{{.ID}} {{.Label "forja.owner"}}' \
     | awk 'NF == 1 {print $1}' | xargs -r docker rm -f
   docker network ls --filter label=forja.run --format '{{.ID}} {{.Label "forja.owner"}}' \
     | awk 'NF == 1 {print $1}' | xargs -r docker network rm
   ```

   The warning that `FORJA_STATE_DIR` is not set now says that a crashed
   run's Docker objects are not removed either.

## Consequences

- A state directory copied to another host (for example, baked into a VM
  image) copies the id. Two live instances would then sweep each other's
  objects. Each instance needs a directory of its own, as ADR 0076 already
  requires for the lock.
- Deleting `FORJA_STATE_DIR/instance` gives the instance a new id. The
  objects of the old id are then nobody's, and the operator removes them as
  in decision 6.
- The sweep takes one list call per object kind and one removal per object.
  On a daemon with no leftovers that is two calls.
- A run that is alive in another process with the same id cannot exist
  while the lock holds. On a mount where `flock` does not exclude (ADR 0076,
  Consequences), it can, and the sweep would remove that run's objects.
