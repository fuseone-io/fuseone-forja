package workspace_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/proxy/registry"
	"github.com/fuseone-io/fuseone-forja/internal/workspace"
)

// lateCancel stands between the driver and the daemon. The first request
// match accepts reaches the daemon whatever its context, so the daemon
// completes it; the run is then cancelled, and the request fails with its
// context's error when that context was the run's. That is a create the
// daemon finished whose answer the driver never read. Every create sent
// after it is counted.
type lateCancel struct {
	base http.RoundTripper

	mu     sync.Mutex
	match  func(*http.Request) bool
	cancel context.CancelFunc
	fired  bool
	after  int
}

func (l *lateCancel) arm(match func(*http.Request) bool, cancel context.CancelFunc) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.match, l.cancel = match, cancel
}

// take reports whether req is the armed one, and counts a create that
// comes after it.
func (l *lateCancel) take(req *http.Request) (context.CancelFunc, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fired && isCreate(req) {
		l.after++
	}
	if l.match == nil || !l.match(req) {
		return nil, false
	}
	l.match, l.fired = nil, true
	return l.cancel, true
}

func (l *lateCancel) state() (fired bool, after int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fired, l.after
}

func (l *lateCancel) RoundTrip(req *http.Request) (*http.Response, error) {
	cancel, armed := l.take(req)
	if !armed {
		return l.base.RoundTrip(req)
	}
	sent, stop := context.WithTimeout(context.WithoutCancel(req.Context()), time.Minute)
	resp, err := l.base.RoundTrip(req.WithContext(sent))
	cancel()
	if err != nil {
		stop()
		return nil, err
	}
	resp.Body = stopOnClose{ReadCloser: resp.Body, stop: stop}
	if ctxErr := req.Context().Err(); ctxErr != nil {
		_ = resp.Body.Close()
		return nil, ctxErr
	}
	return resp, nil
}

// stopOnClose ends the armed request's own context once its body is closed.
type stopOnClose struct {
	io.ReadCloser
	stop context.CancelFunc
}

func (s stopOnClose) Close() error {
	defer s.stop()
	return s.ReadCloser.Close()
}

func isCreate(req *http.Request) bool {
	return strings.HasSuffix(req.URL.Path, "/containers/create") || strings.HasSuffix(req.URL.Path, "/networks/create")
}

// networkCreate matches the creation of the run's network.
func networkCreate(req *http.Request) bool {
	return strings.HasSuffix(req.URL.Path, "/networks/create")
}

// containerCreate matches the creation of the container whose name ends
// with suffix; an empty suffix matches an unnamed one.
func containerCreate(suffix string) func(*http.Request) bool {
	return func(req *http.Request) bool {
		name := req.URL.Query().Get("name")
		if !strings.HasSuffix(req.URL.Path, "/containers/create") {
			return false
		}
		if suffix == "" {
			return name == ""
		}
		return strings.HasSuffix(name, suffix)
	}
}

// lateCancelDocker is newDockerWith speaking to the daemon through the
// returned lateCancel.
func lateCancelDocker(t *testing.T, opts workspace.Options) (*workspace.Docker, *lateCancel) {
	t.Helper()
	newDocker(t)
	probe := dockerCLI(t)
	dial := probe.Dialer()
	base := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) }}
	t.Cleanup(base.CloseIdleConnections)
	l := &lateCancel{base: base}
	cli, err := client.New(client.FromEnv, client.WithHTTPClient(&http.Client{Transport: l}))
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	opts.GatewayBinary = gatewayBinary(t)
	if opts.Runtime == "" {
		opts.Runtime = suiteRuntime(t)
	}
	return workspace.NewDockerOn(cli, opts), l
}

// leftRun is every container, in any state, and every network the daemon
// holds labelled with runID.
func leftRun(t *testing.T, runID string) (containers, networks []string) {
	t.Helper()
	filter := client.Filters{}.Add("label", "forja.run="+runID)
	cs, err := dockerCLI(t).ContainerList(context.Background(), client.ContainerListOptions{All: true, Filters: filter})
	if err != nil {
		t.Fatalf("ContainerList: %v", err)
	}
	for _, c := range cs.Items {
		containers = append(containers, c.ID)
	}
	ns, err := dockerCLI(t).NetworkList(context.Background(), client.NetworkListOptions{Filters: filter})
	if err != nil {
		t.Fatalf("NetworkList: %v", err)
	}
	for _, n := range ns.Items {
		networks = append(networks, n.ID)
	}
	return containers, networks
}

// assertNothingOfRun asserts the daemon holds nothing of runID, and
// removes what it found so a failure does not outlive the test.
func assertNothingOfRun(t *testing.T, runID string) {
	t.Helper()
	containers, networks := leftRun(t, runID)
	if len(containers)+len(networks) > 0 {
		t.Errorf("run %s left behind containers %v and networks %v", runID, containers, networks)
	}
	cli, ctx := dockerCLI(t), context.Background()
	for _, id := range containers {
		_, _ = cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	}
	for _, id := range networks {
		_, _ = cli.NetworkRemove(ctx, id, client.NetworkRemoveOptions{})
	}
}

// A run cancelled while one of its creates is in flight, which the daemon
// completes anyway, leaves nothing behind once the launch gave up.
func TestDocker_cancelledWhileCreating_launchLeavesNothing(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		match func(*http.Request) bool
		agent bool
	}{
		{name: "network", match: networkCreate},
		{name: "gateway", match: containerCreate("-gateway")},
		{name: "registry proxy", match: containerCreate("-registry")},
		{name: "model proxy", match: containerCreate("-model"), agent: true},
		{name: "workspace", match: containerCreate("-workspace")},
		{name: "agent workspace", match: containerCreate("-workspace"), agent: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d, l := lateCancelDocker(t, workspace.Options{
				AgentBinary:         agentBinaryFile(t),
				Model:               workspace.ModelOptions{Key: fakeKey, Upstream: "http://127.0.0.1:9", Cap: 10},
				RegistryCredentials: map[string]registry.Credential{testRegistry: {Username: "forja-reader", Password: canary(t)}},
			})
			runID := newRunID(t)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			l.arm(tt.match, cancel)

			c, err := launchFor(ctx, d, runID, tt.agent, treeWith(t))
			if err == nil {
				_ = c.Close(context.Background())
				t.Fatal("launch = nil error, want the run's cancellation")
			}
			if fired, _ := l.state(); !fired {
				t.Fatalf("the %s create was never sent: %v", tt.name, err)
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("launch = %v, want context.Canceled", err)
			}
			assertNothingOfRun(t, runID)
		})
	}
}

// launchFor is an agent launch, which has a model proxy, or a plain one
// that declares testRegistry, which has a registry proxy.
func launchFor(ctx context.Context, d *workspace.Docker, runID string, agent bool, tree string) (*workspace.Container, error) {
	if agent {
		return d.LaunchAgent(ctx, workspace.AgentLaunch{RunID: runID, Image: testImage, Tree: tree})
	}
	return d.Launch(ctx, workspace.LaunchSpec{RunID: runID, Image: testImage, Tree: tree, Registries: []string{testRegistry}})
}

// A sidecar whose create the daemon completed after the run was cancelled
// is still removed by Close.
func TestDocker_cancelledWhileCreatingService_closeLeavesNothing(t *testing.T) {
	t.Parallel()
	d, l := lateCancelDocker(t, workspace.Options{})
	runID := newRunID(t)
	c, err := d.Launch(context.Background(), workspace.LaunchSpec{RunID: runID, Image: testImage, Tree: treeWith(t)})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	l.arm(containerCreate(""), cancel)

	if _, err := c.StartService(ctx, "db", domain.Service{Image: testImage}, time.Minute); !errors.Is(err, context.Canceled) {
		t.Errorf("StartService = %v, want context.Canceled", err)
	}
	if fired, _ := l.state(); !fired {
		t.Fatal("the service create was never sent")
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer closeCancel()
	if err := c.Close(closeCtx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertNothingOfRun(t, runID)
}

// Once the run is cancelled, no further create reaches the daemon.
func TestDocker_cancelledAfterCreate_nextCreateNeverSent(t *testing.T) {
	t.Parallel()
	d, l := lateCancelDocker(t, workspace.Options{})
	runID := newRunID(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	l.arm(networkCreate, cancel)

	if c, err := d.Launch(ctx, workspace.LaunchSpec{RunID: runID, Image: testImage, Tree: treeWith(t)}); err == nil {
		_ = c.Close(context.Background())
		t.Fatal("Launch = nil error, want the run's cancellation")
	}
	if fired, after := l.state(); !fired || after != 0 {
		t.Errorf("network create sent = %v, creates sent after the cancellation = %d, want true and 0", fired, after)
	}
	assertNothingOfRun(t, runID)
}
