package workspace_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/proxy/registry"
	"github.com/fuseone-io/fuseone-forja/internal/workspace"
)

// removeRunAfter removes whatever runID left on the daemon once the test
// ends, whether or not the test got to its own assertions.
func removeRunAfter(t *testing.T, runID string) {
	t.Helper()
	t.Cleanup(func() {
		containers, networks := leftRun(t, runID)
		cli, ctx := dockerCLI(t), context.Background()
		for _, id := range containers {
			_, _ = cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		}
		for _, id := range networks {
			_, _ = cli.NetworkRemove(ctx, id, client.NetworkRemoveOptions{})
		}
	})
}

// plantCreated leaves what a create the driver never heard back from
// leaves: the run's internal network, and a container on it that was
// created and never started, both labelled with labels.
func plantCreated(t *testing.T, runID string, labels map[string]string) {
	t.Helper()
	removeRunAfter(t, runID)
	cli, ctx := dockerCLI(t), context.Background()
	netName := "forja-run-" + runID
	if _, err := cli.NetworkCreate(ctx, netName, client.NetworkCreateOptions{Driver: "bridge", Internal: true, Labels: labels}); err != nil {
		t.Fatalf("NetworkCreate: %v", err)
	}
	plantContainer(t, runID, labels, netName)
}

// plantContainer leaves a container of runID created on network and never
// started, labelled with labels.
func plantContainer(t *testing.T, runID string, labels map[string]string, network string) {
	t.Helper()
	removeRunAfter(t, runID)
	_, err := dockerCLI(t).ContainerCreate(context.Background(), client.ContainerCreateOptions{
		Name:       "forja-run-" + runID + "-db",
		Config:     &container.Config{Image: testImage, Cmd: []string{"sleep", "infinity"}, Labels: labels},
		HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(network)},
	})
	if err != nil {
		t.Fatalf("ContainerCreate: %v", err)
	}
}

// owned are the labels the driver of owner puts on an object of runID.
func owned(runID, owner string) map[string]string {
	return map[string]string{"forja.run": runID, "forja.owner": owner}
}

// sweepOf sweeps d under a deadline.
func sweepOf(t *testing.T, d *workspace.Docker) (workspace.Swept, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return d.Sweep(ctx)
}

// Every object a launch of this instance creates, the network, the
// gateway, the model and registry proxies, the workspace and a sidecar,
// a create that never came back, and a container whose network is already
// gone, are removed by the next start's sweep when the run died without
// its Close. Each run is reported once, whichever of its objects went.
func TestDocker_sweep_deadRunsOfThisInstance_everyObjectRemoved(t *testing.T) {
	t.Parallel()
	owner := newRunID(t)
	d := newDockerWith(t, workspace.Options{
		Owner:               owner,
		AgentBinary:         agentBinaryFile(t),
		Model:               workspace.ModelOptions{Key: fakeKey, Upstream: "http://127.0.0.1:9", Cap: 10},
		RegistryCredentials: map[string]registry.Credential{testRegistry: {Username: "forja-reader", Password: canary(t)}},
	})
	agentRun, registryRun, createdRun := newRunID(t), newRunID(t), newRunID(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for _, run := range []struct {
		id    string
		agent bool
	}{{agentRun, true}, {registryRun, false}} {
		removeRunAfter(t, run.id)
		c, err := launchFor(ctx, d, run.id, run.agent, treeWith(t))
		if err != nil {
			t.Fatalf("launch of run %s: %v", run.id, err)
		}
		if run.agent {
			if _, err := c.StartService(ctx, "db", domain.Service{Image: testImage}, time.Minute); err != nil {
				t.Fatalf("StartService: %v", err)
			}
		}
	}
	plantCreated(t, createdRun, owned(createdRun, owner))
	unnetworkedRun := newRunID(t)
	plantContainer(t, unnetworkedRun, owned(unnetworkedRun, owner), "none")
	wantRuns := []string{agentRun, registryRun, createdRun, unnetworkedRun}
	var containers int
	for _, id := range wantRuns {
		cs, _ := leftRun(t, id)
		containers += len(cs)
	}

	swept, err := sweepOf(t, d)

	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	slices.Sort(wantRuns)
	if !slices.Equal(swept.Runs, wantRuns) || swept.Containers != containers || swept.Networks != 3 {
		t.Errorf("Sweep = %+v, want runs %v, %d containers and 3 networks", swept, wantRuns, containers)
	}
	for _, id := range wantRuns {
		assertNothingOfRun(t, id)
	}
}

// The sweep removes only what this instance labelled as its own: another
// instance's live run on the same daemon, and an object that names no
// owner, are left as they are.
func TestDocker_sweep_objectsNotThisInstances_untouched(t *testing.T) {
	t.Parallel()
	mine := newDockerWith(t, workspace.Options{Owner: newRunID(t)})
	other := newDockerWith(t, workspace.Options{Owner: newRunID(t)})
	otherRun, bareRun := newRunID(t), newRunID(t)
	removeRunAfter(t, otherRun)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c, err := other.Launch(ctx, workspace.LaunchSpec{RunID: otherRun, Image: testImage, Tree: treeWith(t)})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	plantCreated(t, bareRun, map[string]string{"forja.run": bareRun})
	otherContainers, otherNetworks := leftRun(t, otherRun)

	swept, err := sweepOf(t, mine)

	if err != nil || swept.Containers+swept.Networks != 0 || len(swept.Runs) != 0 {
		t.Errorf("Sweep = %+v, %v; want nothing removed", swept, err)
	}
	if cs, ns := leftRun(t, otherRun); !slices.Equal(cs, otherContainers) || !slices.Equal(ns, otherNetworks) {
		t.Errorf("another instance's run went from %v %v to %v %v", otherContainers, otherNetworks, cs, ns)
	}
	if res, err := c.Exec(ctx, "true"); err != nil || res.ExitCode != 0 {
		t.Errorf("another instance's workspace after the sweep: exec = %+v, %v; want it still running", res, err)
	}
	if cs, ns := leftRun(t, bareRun); len(cs) != 1 || len(ns) != 1 {
		t.Errorf("objects with no owner: %v %v, want the container and the network left", cs, ns)
	}
}

// A driver with no owner refuses to sweep before it asks the daemon
// anything: with no owner, every run's objects would match.
func TestDocker_sweep_noOwner_refusedWithoutCallingTheDaemon(t *testing.T) {
	t.Parallel()
	cli, err := client.New(client.WithHost("tcp://127.0.0.1:1"))
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	d := workspace.NewDockerOn(cli, workspace.Options{})

	_, err = d.Sweep(context.Background())

	if !errors.Is(err, workspace.ErrNoOwner) {
		t.Fatalf("Sweep = %v, want ErrNoOwner", err)
	}
}
