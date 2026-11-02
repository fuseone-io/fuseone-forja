package workspace_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/fuseone-io/fuseone-forja/internal/proxy/registry"
	"github.com/fuseone-io/fuseone-forja/internal/workspace"
)

// envRuntime names the container runtime the whole suite runs under.
// Unset, it is the daemon's default.
const envRuntime = "FORJA_WORKSPACE_RUNTIME"

// registeredRuntimes is the daemon's runtimes, sorted, and its default.
func registeredRuntimes(t *testing.T) ([]string, string) {
	t.Helper()
	info, err := dockerCLI(t).Info(context.Background(), client.InfoOptions{})
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	return slices.Sorted(maps.Keys(info.Info.Runtimes)), info.Info.DefaultRuntime
}

// suiteRuntime is the runtime envRuntime names. One the daemon does not
// list fails the test rather than skipping it: a skip would let the suite
// pass under the new runtime with nothing run.
func suiteRuntime(t *testing.T) string {
	t.Helper()
	want := os.Getenv(envRuntime)
	if want == "" {
		return ""
	}
	if names, _ := registeredRuntimes(t); !slices.Contains(names, want) {
		t.Fatalf("%s=%s but the daemon lists only %v", envRuntime, want, names)
	}
	return want
}

// explicitRuntime is suiteRuntime when set, or else a runtime the daemon
// lists that is not its default: the daemon records its default on every
// container created without one, so only another name tells a container
// given the runtime from one left without it.
func explicitRuntime(t *testing.T) string {
	t.Helper()
	if want := suiteRuntime(t); want != "" {
		return want
	}
	names, def := registeredRuntimes(t)
	for _, name := range names {
		if name != def {
			return name
		}
	}
	t.Fatalf("the daemon lists no runtime besides its default %q: nothing tells a configured runtime from none", def)
	return ""
}

// runContainers inspects every container labelled with runID, running or
// not.
func runContainers(t *testing.T, runID string) []container.InspectResponse {
	t.Helper()
	list, err := dockerCLI(t).ContainerList(context.Background(), client.ContainerListOptions{
		All: true, Filters: client.Filters{}.Add("label", "forja.run="+runID),
	})
	if err != nil {
		t.Fatalf("ContainerList: %v", err)
	}
	var out []container.InspectResponse
	for _, item := range list.Items {
		out = append(out, inspect(t, item.ID))
	}
	return out
}

// A runtime the daemon does not list refuses the launch before an image is
// pulled, a binary looked for, or anything of the run created.
func TestDocker_unknownRuntime_launchRefusedNothingCreated(t *testing.T) {
	newDocker(t)
	d, err := workspace.NewDocker(workspace.Options{Runtime: "forja-no-such-runtime", GatewayBinary: filepath.Join(t.TempDir(), "no-forjad")})
	if err != nil {
		t.Fatal(err)
	}
	runID := newRunID(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	_, err = d.Launch(ctx, workspace.LaunchSpec{RunID: runID, Image: "forja-no-such-image-0000:none", Tree: treeWith(t)})
	if !errors.Is(err, workspace.ErrRuntimeUnavailable) || !strings.Contains(err.Error(), "forja-no-such-runtime") {
		t.Fatalf("Launch = %v, want ErrRuntimeUnavailable naming forja-no-such-runtime", err)
	}
	if n := leftovers(t, runID); n != 0 {
		t.Fatalf("%d containers or networks created for a refused launch", n)
	}
}

// The workspace, its sidecar, its gateway and its registry proxy all run
// under the configured runtime: a compromised workspace reaches the
// proxies over the network.
func TestDocker_runtime_everyContainerOfTheRun(t *testing.T) {
	newDocker(t)
	want := explicitRuntime(t)
	d := newDockerWith(t, workspace.Options{Runtime: want, RegistryCredentials: map[string]registry.Credential{
		testRegistry: {Username: "forja-reader", Password: canary(t)},
	}})
	runID := newRunID(t)
	c := launchSpec(t, d, workspace.LaunchSpec{RunID: runID, Image: testImage, Tree: treeWith(t), Registries: []string{testRegistry}})
	startReady(t, c, "cache", cache)

	all := runContainers(t, runID)
	if len(all) != 4 {
		t.Fatalf("%d containers labelled with the run, want the workspace, the gateway, the registry proxy and the sidecar", len(all))
	}
	for _, ct := range all {
		if ct.HostConfig.Runtime != want {
			t.Errorf("container %s runs under %q, want %q", ct.Name, ct.HostConfig.Runtime, want)
		}
	}
}

// isolationDocker is a driver whose runs each have a registry proxy and a
// gateway with a way out, so both proxies sit on the default bridge.
func isolationDocker(t *testing.T) *workspace.Docker {
	t.Helper()
	return newDockerWith(t, workspace.Options{
		EgressAllow:         []string{"forja-unresolvable.invalid:443"},
		RegistryCredentials: map[string]registry.Credential{testRegistry: {Username: "forja-reader", Password: canary(t)}},
	})
}

// launchWithPostgres launches runID with testRegistry declared and starts a
// postgres sidecar in it. It runs off the test goroutine, so it returns
// what failed instead of failing the test; a container it launched is
// returned either way, for the caller to close.
func launchWithPostgres(d *workspace.Docker, runID, tree string) (*workspace.Container, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c, err := d.Launch(ctx, workspace.LaunchSpec{RunID: runID, Image: testImage, Tree: tree, Registries: []string{testRegistry}})
	if err != nil {
		return nil, fmt.Errorf("launch %s: %w", runID, err)
	}
	res, err := c.StartService(ctx, "postgres", postgres, time.Minute)
	if err != nil || !res.Ready {
		return c, fmt.Errorf("postgres of %s = {ready %v %s}, %v; want ready", runID, res.Ready, res.Reason, err)
	}
	return c, nil
}

// runFootprint is what one run's containers hold of the daemon: the names
// of their volumes and of their networks, as the daemon names them.
type runFootprint struct {
	volumes, networks map[string]bool
}

// footprint checks no container of runID has a bind mount and its
// workspace no mount at all, that only its proxies are on the default
// bridge, and returns what the run holds.
func footprint(t *testing.T, runID, workspaceID string) runFootprint {
	t.Helper()
	fp := runFootprint{volumes: map[string]bool{}, networks: map[string]bool{}}
	proxies := []string{"/" + runGateway(runID), "/" + runRegistry(runID)}
	for _, ct := range runContainers(t, runID) {
		if ct.ID == workspaceID && len(ct.Mounts) != 0 {
			t.Errorf("workspace of %s has mounts %+v, want none: the tree is copied", runID, ct.Mounts)
		}
		for _, m := range ct.Mounts {
			if m.Type == mount.TypeBind {
				t.Errorf("container %s binds %s from the host", ct.Name, m.Source)
			}
			if m.Type == mount.TypeVolume {
				fp.volumes[m.Name] = true
			}
		}
		for name := range ct.NetworkSettings.Networks {
			if name == "bridge" && !slices.Contains(proxies, ct.Name) {
				t.Errorf("container %s of %s is on the default bridge, which only its proxies may share", ct.Name, runID)
			}
			fp.networks[name] = true
		}
	}
	return fp
}

// Two runs at once hold no volume and no network in common, but for the
// default bridge their proxies reach the outside through.
func TestDocker_twoConcurrentRuns_shareNoVolumeOrNetwork(t *testing.T) {
	d := isolationDocker(t)
	runs := [2]string{newRunID(t), newRunID(t)}
	containers := launchConcurrently(t, d, runs)
	a, b := footprint(t, runs[0], containers[0].ID()), footprint(t, runs[1], containers[1].ID())

	if len(a.volumes) == 0 || len(b.volumes) == 0 {
		t.Fatalf("volumes = %v and %v, want postgres's own in each: the comparison proves nothing", a.volumes, b.volumes)
	}
	if both := common(a.volumes, b.volumes); len(both) != 0 {
		t.Errorf("volumes held by both runs: %v", both)
	}
	if both := common(a.networks, b.networks); !slices.Equal(both, []string{"bridge"}) {
		t.Errorf("networks shared by both runs = %v, want exactly [bridge]", both)
	}
}

// launchConcurrently launches each run with a postgres sidecar at the same
// time and closes whatever was launched when the test ends.
func launchConcurrently(t *testing.T, d *workspace.Docker, runs [2]string) [2]*workspace.Container {
	t.Helper()
	var containers [2]*workspace.Container
	var errs [2]error
	var wg sync.WaitGroup
	for i := range runs {
		tree := treeWith(t)
		wg.Go(func() { containers[i], errs[i] = launchWithPostgres(d, runs[i], tree) })
	}
	wg.Wait()
	for _, c := range containers {
		if c != nil {
			t.Cleanup(func() { _ = c.Close(context.Background()) })
		}
	}
	if err := errors.Join(errs[:]...); err != nil {
		t.Fatal(err)
	}
	return containers
}

// common is the sorted names in both sets.
func common(a, b map[string]bool) []string {
	var both []string
	for name := range a {
		if b[name] {
			both = append(both, name)
		}
	}
	slices.Sort(both)
	return both
}
