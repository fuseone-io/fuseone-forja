package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/gates"
	"github.com/fuseone-io/fuseone-forja/internal/workspace"
)

// The names the driver gives what it creates for a run.
func runNetwork(runID string) string { return "forja-run-" + runID }
func runGateway(runID string) string { return "forja-run-" + runID + "-gateway" }

// runGate runs one gate through the real gates runner in a workspace of
// runID, the way forja.doctor does.
func runGate(t *testing.T, d *workspace.Docker, runID, command string) domain.GateReport {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	runner := &gates.Runner{Launcher: pilotLauncher{d}, Now: time.Now}
	c := domain.Contract{Image: testImage, Gates: []domain.Gate{{Name: "fetch", Run: command}}}
	report, err := runner.Run(ctx, runID, c, treeWith(t))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return report
}

// A gate that goes for the internet fails by its own exit code, and the
// attempt is in the report as the control plane read it from the gateway.
func TestDocker_gateFetchesInternet_exitNonZeroAndDenialRecorded(t *testing.T) {
	tests := []struct {
		name, command, destination string
		kind                       domain.EgressKind
	}{
		// busybox wget honours http_proxy for an http URL: the gateway gets
		// an absolute-form GET.
		{name: "http", command: "wget -T 5 -O- http://example.com/some/path",
			destination: "example.com:80", kind: domain.EgressHTTP},
		// busybox wget ignores https_proxy, so the tunnel is asked for by
		// hand, exactly as curl asks for it.
		{name: "https", command: `printf 'CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n' | nc -w 5 forja-gateway 3128 | head -n 1 | grep -q ' 200 '`,
			destination: "example.com:443", kind: domain.EgressConnect},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newDocker(t)

			report := runGate(t, d, newRunID(t), tt.command)
			fetch := report.Gates[0]
			if fetch.Status != domain.StepFailed || fetch.ExitCode == 0 || report.OK() {
				t.Fatalf("gate = {%s exit %d}, report OK %v; want failed by its exit code\nstderr: %s", fetch.Status, fetch.ExitCode, report.OK(), fetch.Stderr)
			}
			if report.Egress.Count != 1 || len(report.Egress.Denied) != 1 {
				t.Fatalf("egress = %+v, want exactly one denial", report.Egress)
			}
			got := report.Egress.Denied[0]
			if got.Destination != tt.destination || got.Kind != tt.kind || got.At.IsZero() {
				t.Fatalf("denial = %+v, want %s %s with a time", got, tt.kind, tt.destination)
			}
		})
	}
}

// sibling starts a container that listens on 8080 on the run's network
// under name, the way a sidecar will, and removes it before the run's
// network goes.
func sibling(t *testing.T, runID, name string) {
	t.Helper()
	cli := dockerCLI(t)
	ctx := context.Background()
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:       name,
		Config:     &container.Config{Image: testImage, Cmd: []string{"nc", "-lk", "-p", "8080", "-e", "true"}},
		HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(runNetwork(runID))},
	})
	if err != nil {
		t.Fatalf("create sibling %s: %v", name, err)
	}
	t.Cleanup(func() {
		_, _ = cli.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	})
	if _, err := cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start sibling %s: %v", name, err)
	}
}

// resolve looks a name up through libc, as the gate's own tools do. busybox
// nslookup walks the host's search list whatever ndots says, and a search
// domain the internal network cannot forward answers SERVFAIL.
const resolve = "getent hosts "

func mustExec(t *testing.T, c *workspace.Container, command string) workspace.ExecResult {
	t.Helper()
	res, err := c.Exec(context.Background(), command)
	if err != nil {
		t.Fatalf("Exec(%s): %v", command, err)
	}
	return res
}

// The workspace reaches its gateway and its run's siblings by name, and
// the gateway alone: the network has no route out.
func TestDocker_workspace_reachesGatewayAndRunSiblingByName(t *testing.T) {
	d := newDocker(t)
	runID := newRunID(t)
	c := launch(t, d, runID, treeWith(t))
	name := "sib-" + runID
	sibling(t, runID, name)

	for _, command := range []string{
		resolve + "forja-gateway", "nc -z -w 3 forja-gateway 3128",
		resolve + name, "nc -z -w 3 " + name + " 8080",
	} {
		if res := mustExec(t, c, command); res.ExitCode != 0 {
			t.Errorf("%s: exit %d, want 0\n%s%s", command, res.ExitCode, res.Stdout, res.Stderr)
		}
	}
}

// A socket that skips the proxy goes nowhere: the run's network is
// internal. Such an attempt is not in the gateway's log.
func TestDocker_rawSocketToExternalIP_denied(t *testing.T) {
	d := newDocker(t)
	c := launch(t, d, newRunID(t), treeWith(t))

	if res := mustExec(t, c, "nc -z -w 3 1.1.1.1 80"); res.ExitCode == 0 {
		t.Fatal("nc to 1.1.1.1:80 from the workspace exit 0, want the network unreachable")
	}
	if res := mustExec(t, c, resolve+"example.com"); res.ExitCode == 0 {
		t.Fatalf("%sexample.com from the workspace exit 0, want no external name resolved:\n%s", resolve, res.Stdout)
	}
}

// gatewayIP is the address of runID's gateway on its own run's network.
func gatewayIP(t *testing.T, runID string) string {
	t.Helper()
	inspect, err := dockerCLI(t).ContainerInspect(context.Background(), runGateway(runID), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect gateway of %s: %v", runID, err)
	}
	ep, ok := inspect.Container.NetworkSettings.Networks[runNetwork(runID)]
	if !ok || !ep.IPAddress.IsValid() {
		t.Fatalf("gateway of %s has no address on its run network: %+v", runID, inspect.Container.NetworkSettings.Networks)
	}
	return ep.IPAddress.String()
}

func TestDocker_twoRuns_cannotResolveOrReachEachOthersSiblings(t *testing.T) {
	d := newDocker(t)
	runs := [2]string{newRunID(t), newRunID(t)}
	var containers [2]*workspace.Container
	var wg sync.WaitGroup
	for i := range runs {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			c, err := d.Launch(ctx, workspace.LaunchSpec{RunID: runs[i], Image: testImage, Tree: treeWith(t)})
			if err != nil {
				t.Errorf("Launch %s: %v", runs[i], err)
				return
			}
			containers[i] = c
		})
	}
	wg.Wait()
	for _, c := range containers {
		if c == nil {
			t.FailNow()
		}
		t.Cleanup(func() { _ = c.Close(context.Background()) })
	}
	sibling(t, runs[0], "sib-"+runs[0])
	sibling(t, runs[1], "sib-"+runs[1])
	a, otherSibling := containers[0], "sib-"+runs[1]

	if res := mustExec(t, a, resolve+otherSibling); res.ExitCode == 0 {
		t.Errorf("run A resolved run B's sibling:\n%s", res.Stdout)
	}
	if res := mustExec(t, a, "nc -z -w 3 "+gatewayIP(t, runs[1])+" 3128"); res.ExitCode == 0 {
		t.Error("run A reached run B's gateway by its address on B's network")
	}
	res := mustExec(t, a, resolve+"forja-gateway")
	if own := gatewayIP(t, runs[0]); res.ExitCode != 0 || !strings.Contains(string(res.Stdout), own) || strings.Contains(string(res.Stdout), gatewayIP(t, runs[1])) {
		t.Errorf("forja-gateway in run A = exit %d\n%s\nwant A's own gateway %s only", res.ExitCode, res.Stdout, own)
	}
}

// The gateway runs as nobody with no capability, and has a way out only
// when the operator listed somewhere to go. The workspace is on its run's
// network alone.
func TestDocker_gateway_unprivilegedAndBridgedOnlyWhenAllowed(t *testing.T) {
	tests := []struct {
		name     string
		allow    []string
		networks []string
	}{
		{name: "empty allowlist", allow: nil, networks: []string{"run"}},
		{name: "listed host", allow: []string{"proxy.golang.org:443"}, networks: []string{"bridge", "run"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("FORJA_GITHUB_TOKEN", "secret-github-token-0123")
			d := newDocker(t, tt.allow...)
			runID := newRunID(t)
			c := launch(t, d, runID, treeWith(t))

			gw := inspect(t, runGateway(runID))
			if gw.Config.User != "65534" || !slices.Equal(gw.HostConfig.CapDrop, []string{"ALL"}) || !slices.Contains(gw.HostConfig.SecurityOpt, "no-new-privileges") {
				t.Errorf("gateway user %q capdrop %v securityopt %v, want 65534, [ALL], no-new-privileges", gw.Config.User, gw.HostConfig.CapDrop, gw.HostConfig.SecurityOpt)
			}
			assertNoSecrets(t, "gateway", strings.Join(gw.Config.Env, "\n"))
			if got := networksOf(gw, runID); !slices.Equal(got, tt.networks) {
				t.Errorf("gateway networks = %v, want %v", got, tt.networks)
			}
			if got := networksOf(inspect(t, c.ID()), runID); !slices.Equal(got, []string{"run"}) {
				t.Errorf("workspace networks = %v, want its run's network only", got)
			}
		})
	}
}

func inspect(t *testing.T, id string) container.InspectResponse {
	t.Helper()
	res, err := dockerCLI(t).ContainerInspect(context.Background(), id, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("inspect %s: %v", id, err)
	}
	return res.Container
}

// networksOf lists the container's networks, the run's own as "run".
func networksOf(c container.InspectResponse, runID string) []string {
	var names []string
	for name := range c.NetworkSettings.Networks {
		if name == runNetwork(runID) {
			name = "run"
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func TestDocker_close_removesWorkspaceGatewayAndNetwork(t *testing.T) {
	d := newDocker(t)
	runID := newRunID(t)
	c := launch(t, d, runID, treeWith(t))
	ctx := context.Background()

	if err := c.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertGone(t, c.ID())
	assertGone(t, runGateway(runID))
	if _, err := dockerCLI(t).NetworkInspect(ctx, runNetwork(runID), client.NetworkInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Fatalf("NetworkInspect after Close = %v, want not found", err)
	}
	if err := c.Close(ctx); err != nil {
		t.Fatalf("second Close: %v, want nil: Close is idempotent", err)
	}
	if _, err := c.Exec(ctx, "true"); err == nil {
		t.Fatal("Exec after Close = nil error, want a failure")
	}
	if _, err := c.Egress(ctx); err == nil {
		t.Fatal("Egress after Close = nil error, want a failure: the gateway's log is gone")
	}
}

// leftovers counts what the daemon still holds labelled with runID:
// containers, running or not, and networks.
func leftovers(t *testing.T, runID string) int {
	t.Helper()
	cli, ctx := dockerCLI(t), context.Background()
	filter := client.Filters{}.Add("label", "forja.run="+runID)
	containers, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filter})
	if err != nil {
		t.Fatalf("ContainerList: %v", err)
	}
	networks, err := cli.NetworkList(ctx, client.NetworkListOptions{Filters: filter})
	if err != nil {
		t.Fatalf("NetworkList: %v", err)
	}
	return len(containers.Items) + len(networks.Items)
}

func TestDocker_launchFails_noNetworkOrGatewayLeft(t *testing.T) {
	tests := []struct {
		name  string
		image string
		tree  func(t *testing.T) string
	}{
		{name: "image unknown", image: "forja-no-such-image-0000:none", tree: treeWith},
		{name: "tree missing", image: testImage, tree: func(t *testing.T) string { return filepath.Join(t.TempDir(), "no-such-tree") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newDocker(t)
			runID := newRunID(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()

			if _, err := d.Launch(ctx, workspace.LaunchSpec{RunID: runID, Image: tt.image, Tree: tt.tree(t)}); err == nil {
				t.Fatal("Launch = nil error, want a failure")
			}
			if n := leftovers(t, runID); n != 0 {
				t.Fatalf("%d containers or networks of the failed launch left behind", n)
			}
		})
	}
}

func TestDocker_noGatewayBinary_launchRefusedNothingCreated(t *testing.T) {
	newDocker(t)
	d, err := workspace.NewDocker(workspace.Options{GatewayBinary: filepath.Join(t.TempDir(), "no-forjad")})
	if err != nil {
		t.Fatal(err)
	}
	runID := newRunID(t)

	_, err = d.Launch(context.Background(), workspace.LaunchSpec{RunID: runID, Image: testImage, Tree: treeWith(t)})
	if err == nil || !strings.Contains(err.Error(), "no-forjad") {
		t.Fatalf("Launch without a gateway binary = %v, want an error naming it", err)
	}
	if n := leftovers(t, runID); n != 0 {
		t.Fatalf("%d containers or networks created for a launch that had no gateway", n)
	}
}

// A gateway that stopped during the run may have missed denials: the
// report is refused, not handed over short.
func TestDocker_gatewayStopped_egressRefused(t *testing.T) {
	d := newDocker(t)
	runID := newRunID(t)
	c := launch(t, d, runID, treeWith(t))
	ctx := context.Background()
	if _, err := dockerCLI(t).ContainerKill(ctx, runGateway(runID), client.ContainerKillOptions{}); err != nil {
		t.Fatalf("kill gateway: %v", err)
	}

	if report, err := c.Egress(ctx); err == nil {
		t.Fatalf("Egress after the gateway stopped = %+v, nil error; want a failure", report)
	}
}

// A listed destination gets past the policy to the gateway's own dialer:
// the CONNECT is not refused and nothing is recorded. The name never
// resolves, so the tunnel ends upstream with a 502 and nothing leaves.
func TestDocker_listedDestination_reachesGatewayDialerNotDenied(t *testing.T) {
	const listed = "forja-unresolvable.invalid:443"
	d := newDocker(t, listed)

	report := runGate(t, d, newRunID(t), `printf 'CONNECT `+listed+` HTTP/1.1\r\nHost: `+listed+`\r\n\r\n' | nc -w 12 forja-gateway 3128 | head -n 1 | grep -q ' 502 '`)
	if fetch := report.Gates[0]; fetch.Status != domain.StepPassed {
		t.Fatalf("CONNECT to a listed name = {%s exit %d}, want the 502 of an unreachable upstream\nstderr: %s", fetch.Status, fetch.ExitCode, fetch.Stderr)
	}
	if report.Egress.Count != 0 {
		t.Fatalf("egress = %+v, want no denial for a listed destination", report.Egress)
	}
}

// Launch waits for the gateway to say it is serving: a gateway that exits
// first fails the launch, and nothing of the run is left behind.
func TestDocker_gatewayExitsBeforeReady_launchFailsNothingLeft(t *testing.T) {
	newDocker(t)
	script := filepath.Join(t.TempDir(), "forjad")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	d, err := workspace.NewDocker(workspace.Options{GatewayBinary: script})
	if err != nil {
		t.Fatal(err)
	}
	runID := newRunID(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	if c, err := d.Launch(ctx, workspace.LaunchSpec{RunID: runID, Image: testImage, Tree: treeWith(t)}); err == nil {
		_ = c.Close(context.Background())
		t.Fatal("Launch with a gateway that exits before it is ready = nil error, want a failure")
	}
	if n := leftovers(t, runID); n != 0 {
		t.Fatalf("%d containers or networks of the failed launch left behind", n)
	}
}

func TestDocker_noDenial_emptyReport(t *testing.T) {
	d := newDocker(t)
	c := launch(t, d, newRunID(t), treeWith(t))

	report, err := c.Egress(context.Background())
	if err != nil || report.Count != 0 || len(report.Denied) != 0 {
		t.Fatalf("Egress of a quiet run = %+v, %v; want an empty report", report, err)
	}
}
