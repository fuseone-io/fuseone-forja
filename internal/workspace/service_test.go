package workspace_test

import (
	"context"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/workspace"
)

// sidecarImage is small, EXPOSEs 6379 with no HEALTHCHECK of its own and
// listens as soon as it starts.
const sidecarImage = "redis:8-alpine"

// postgresImage is the pilot's sidecar: its first server, during initdb,
// listens on the Unix socket only, so a container that is running is not
// yet one that accepts connections.
const postgresImage = "postgres:16"

var (
	cache    = domain.Service{Image: sidecarImage, Env: map[string]string{}}
	postgres = domain.Service{Image: postgresImage, Env: map[string]string{"POSTGRES_HOST_AUTH_METHOD": "trust"}}
)

func startService(t *testing.T, c *workspace.Container, name string, svc domain.Service, readiness time.Duration) workspace.ServiceResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	res, err := c.StartService(ctx, name, svc, readiness)
	if err != nil {
		t.Fatalf("StartService(%s): %v", name, err)
	}
	return res
}

func startReady(t *testing.T, c *workspace.Container, name string, svc domain.Service) {
	t.Helper()
	if res := startService(t, c, name, svc, time.Minute); !res.Ready {
		t.Fatalf("service %s not ready: %s\n%s", name, res.Reason, res.Log)
	}
}

// reach is the exit code of a TCP connect from the workspace to host:port.
func reach(t *testing.T, c *workspace.Container, hostPort string) int {
	t.Helper()
	host, port, _ := strings.Cut(hostPort, ":")
	res, err := c.Exec(context.Background(), "nc -z -w 2 "+host+" "+port)
	if err != nil {
		t.Fatalf("Exec(nc %s): %v", hostPort, err)
	}
	return res.ExitCode
}

// sidecarIP is the address the sidecar with alias holds on network.
func sidecarIP(t *testing.T, network, alias string) string {
	t.Helper()
	cli := rawClient(t)
	list, err := cli.ContainerList(context.Background(), client.ContainerListOptions{
		Filters: client.Filters{}.Add("network", network),
	})
	if err != nil {
		t.Fatalf("ContainerList: %v", err)
	}
	for _, item := range list.Items {
		inspect, err := cli.ContainerInspect(context.Background(), item.ID, client.ContainerInspectOptions{})
		if err != nil {
			t.Fatalf("ContainerInspect: %v", err)
		}
		for _, ep := range inspect.Container.NetworkSettings.Networks {
			if ep.NetworkID == network && contains(ep.Aliases, alias) {
				return ep.IPAddress.String()
			}
		}
	}
	t.Fatalf("no container with alias %s on network %s", alias, network)
	return ""
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

func TestDocker_service_resolvesByNameFromTheWorkspace(t *testing.T) {
	d := newDocker(t)
	c := launch(t, d, newRunID(t), treeWith(t))
	startReady(t, c, "cache", cache)

	res, err := c.Exec(context.Background(), "printf 'PING\\r\\nQUIT\\r\\n' | nc -w 2 cache 6379")
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("Exec(redis PING via cache) = %v, exit %d, stderr %q", err, res.ExitCode, res.Stderr)
	}
	if !strings.Contains(string(res.Stdout), "+PONG") {
		t.Fatalf("redis answered %q through the name cache, want +PONG", res.Stdout)
	}
}

// Postgres runs for a while before it accepts a TCP connection. Ready
// means listening: the first connect after it, with no retry, succeeds.
func TestDocker_service_readyOnlyWhenListening(t *testing.T) {
	d := newDocker(t)
	c := launch(t, d, newRunID(t), treeWith(t))
	startReady(t, c, "postgres", postgres)

	if code := reach(t, c, "postgres:5432"); code != 0 {
		t.Fatalf("connect to postgres:5432 right after Ready exited %d, want 0", code)
	}
}

func TestDocker_service_notReadyWithinReadiness_resultWithReasonNoError(t *testing.T) {
	d := newDocker(t)
	c := launch(t, d, newRunID(t), treeWith(t))
	// The server honours PGPORT: it runs and listens, but never on the port
	// the image exposes.
	elsewhere := domain.Service{Image: postgresImage, Env: map[string]string{"POSTGRES_HOST_AUTH_METHOD": "trust", "PGPORT": "5433"}}

	start := time.Now()
	res := startService(t, c, "postgres", elsewhere, 8*time.Second)
	if res.Ready {
		t.Fatal("Ready = true for a service that never listens on its exposed port")
	}
	if elapsed := time.Since(start); elapsed < 8*time.Second || elapsed > 40*time.Second {
		t.Errorf("StartService returned after %v, want soon after the 8s readiness", elapsed)
	}
	if want := "service postgres not healthy within 8s"; res.Reason != want {
		t.Errorf("Reason = %q, want %q", res.Reason, want)
	}
	// The server logs to stderr; initdb's banner goes to stdout.
	for _, want := range []string{"port 5433", "The files belonging to this database system"} {
		if !strings.Contains(string(res.Log), want) {
			t.Errorf("Log = %q, want the sidecar's own output on both streams, %q included", res.Log, want)
		}
	}
}

// initdb --debug writes far more than the log keeps, and the server then
// listens off its exposed port, so the sidecar is never ready.
func TestDocker_service_logOverTheCap_tailKeptAndTruncated(t *testing.T) {
	d := newDocker(t)
	c := launch(t, d, newRunID(t), treeWith(t))
	verbose := domain.Service{Image: postgresImage, Env: map[string]string{
		"POSTGRES_HOST_AUTH_METHOD": "trust", "PGPORT": "5433", "POSTGRES_INITDB_ARGS": "--debug",
	}}

	res := startService(t, c, "postgres", verbose, 8*time.Second)
	if res.Ready {
		t.Fatal("Ready = true for a service that never listens on its exposed port")
	}
	if len(res.Log) != workspace.MaxStreamBytes || !res.Truncated {
		t.Fatalf("Log len %d, truncated %v; want %d and true", len(res.Log), res.Truncated, workspace.MaxStreamBytes)
	}
	if !strings.Contains(string(res.Log), "port 5433") {
		t.Errorf("Log tail does not carry the server's last lines: %q", res.Log[len(res.Log)-512:])
	}
}

func TestDocker_service_exits_resultWithExitCodeAndLog(t *testing.T) {
	d := newDocker(t)
	c := launch(t, d, newRunID(t), treeWith(t))
	// Without a password or trust the image refuses to initialise and exits.
	refused := domain.Service{Image: postgresImage, Env: map[string]string{}}

	res := startService(t, c, "postgres", refused, time.Minute)
	if res.Ready || res.Reason != "service postgres exited with code 1" {
		t.Fatalf("result = {ready %v, reason %q}, want not ready, exited with code 1", res.Ready, res.Reason)
	}
	if !strings.Contains(string(res.Log), "POSTGRES_PASSWORD") {
		t.Errorf("Log = %q, want the entrypoint's own explanation", res.Log)
	}
}

func TestDocker_service_imageUnknown_errorNamesIt(t *testing.T) {
	d := newDocker(t)
	c := launch(t, d, newRunID(t), treeWith(t))

	_, err := c.StartService(context.Background(), "ghost", domain.Service{Image: "forja-no-such-image-0000:none"}, time.Minute)
	if err == nil || !strings.Contains(err.Error(), "forja-no-such-image-0000") {
		t.Fatalf("StartService(unknown image) = %v, want an error naming the image", err)
	}
}

func TestDocker_close_removesSidecarsAndNetwork(t *testing.T) {
	d := newDocker(t)
	c := launch(t, d, newRunID(t), treeWith(t))
	startReady(t, c, "cache", cache)
	notReady := startService(t, c, "late", cache, time.Millisecond)
	if notReady.Ready {
		t.Fatal("a 1ms readiness was met; the not-ready sidecar this test needs was not left behind")
	}
	list, err := rawClient(t).ContainerList(context.Background(), client.ContainerListOptions{
		Filters: client.Filters{}.Add("network", c.NetworkID()),
	})
	if err != nil || len(list.Items) != 4 {
		t.Fatalf("containers on the run network = %d (%v), want the workspace, the gateway and two sidecars", len(list.Items), err)
	}
	// The label is how a container a crashed run left behind is found.
	for _, item := range list.Items {
		if _, ok := item.Labels["forja.run"]; !ok {
			t.Errorf("container %s on the run network has no forja.run label: labels %v", item.ID, item.Labels)
		}
	}

	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, item := range list.Items {
		assertGone(t, item.ID)
	}
	assertNetworkGone(t, c.NetworkID())
	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v, want nil: Close is idempotent", err)
	}
}

// A sidecar's anonymous volumes go with it: postgres declares one for its
// data, and nothing of the run may outlive Close.
func TestDocker_close_removesSidecarAnonymousVolumes(t *testing.T) {
	d := newDocker(t)
	runID := newRunID(t)
	c := launch(t, d, runID, treeWith(t))
	startReady(t, c, "postgres", postgres)
	var volumes []string
	for _, ct := range runContainers(t, runID) {
		for _, m := range ct.Mounts {
			if m.Type == mount.TypeVolume {
				volumes = append(volumes, m.Name)
			}
		}
	}
	if len(volumes) == 0 {
		t.Fatal("the postgres sidecar has no volume: the test proves nothing")
	}

	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, name := range volumes {
		if _, err := dockerCLI(t).VolumeInspect(context.Background(), name, client.VolumeInspectOptions{}); !cerrdefs.IsNotFound(err) {
			t.Errorf("VolumeInspect(%s) after Close = %v, want not found", name, err)
		}
	}
}

func TestDocker_service_unreachableFromAnotherRun(t *testing.T) {
	d := newDocker(t)
	a, b := launch(t, d, newRunID(t), treeWith(t)), launch(t, d, newRunID(t), treeWith(t))
	startReady(t, a, "only-a", cache)
	startReady(t, b, "only-b", cache)
	ipA := sidecarIP(t, a.NetworkID(), "only-a")

	for _, own := range []string{"only-a:6379", ipA + ":6379"} {
		if code := reach(t, a, own); code != 0 {
			t.Fatalf("run A cannot reach its own sidecar at %s (exit %d): the probe proves nothing", own, code)
		}
	}
	if code := reach(t, b, "only-b:6379"); code != 0 {
		t.Fatalf("run B cannot reach its own sidecar (exit %d)", code)
	}
	for _, other := range []string{"only-a:6379", ipA + ":6379"} {
		if code := reach(t, b, other); code == 0 {
			t.Errorf("run B reached run A's sidecar at %s", other)
		}
	}
}

// probeFrom runs a throwaway container on networkMode that connects to
// host:port, and returns its exit code.
func probeFrom(t *testing.T, networkMode, host, port string) int64 {
	t.Helper()
	cli := rawClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: testImage, Cmd: []string{"nc", "-z", "-w", "2", host, port}},
		HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(networkMode)},
	})
	if err != nil {
		t.Fatalf("ContainerCreate(probe): %v", err)
	}
	defer func() { _, _ = cli.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true}) }()
	wait := cli.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNextExit})
	if _, err := cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("ContainerStart(probe): %v", err)
	}
	select {
	case res := <-wait.Result:
		return res.StatusCode
	case err := <-wait.Error:
		t.Fatalf("ContainerWait(probe): %v", err)
	case <-ctx.Done():
		t.Fatalf("probe did not finish: %v", ctx.Err())
	}
	return -1
}

// A port published on the host is reachable from outside the run network
// whatever the bridges isolate, so no container on it may have one.
func TestDocker_service_noPortPublishedOnTheHost(t *testing.T) {
	d := newDocker(t)
	c := launch(t, d, newRunID(t), treeWith(t))
	startReady(t, c, "cache", cache)

	cli := rawClient(t)
	list, err := cli.ContainerList(context.Background(), client.ContainerListOptions{
		Filters: client.Filters{}.Add("network", c.NetworkID()),
	})
	if err != nil || len(list.Items) != 3 {
		t.Fatalf("containers on the run network = %d (%v), want the workspace, the gateway and the sidecar", len(list.Items), err)
	}
	for _, item := range list.Items {
		inspect, err := cli.ContainerInspect(context.Background(), item.ID, client.ContainerInspectOptions{})
		if err != nil {
			t.Fatalf("ContainerInspect: %v", err)
		}
		for port, bindings := range inspect.Container.NetworkSettings.Ports {
			if len(bindings) != 0 {
				t.Errorf("container %s publishes %s on the host as %v", item.ID, port, bindings)
			}
		}
	}
}

func TestDocker_service_unreachableFromDefaultBridge(t *testing.T) {
	d := newDocker(t)
	c := launch(t, d, newRunID(t), treeWith(t))
	startReady(t, c, "cache", cache)
	ip := sidecarIP(t, c.NetworkID(), "cache")

	if code := probeFrom(t, c.NetworkID(), ip, "6379"); code != 0 {
		t.Fatalf("a container on the run network cannot reach the sidecar at %s (exit %d): the probe proves nothing", ip, code)
	}
	if code := probeFrom(t, "bridge", ip, "6379"); code == 0 {
		t.Fatalf("a container on the default bridge reached the sidecar at %s", ip)
	}
}
