package workspace_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/workspace"
)

// testImage is small and ships a POSIX sh, busybox wget and nc, which is
// all these tests need.
const testImage = "alpine:3.20"

// gatewayBuild cross-compiles forjad once per test binary, for the
// daemon's architecture, the first time a test that reached a daemon asks
// for it. TestMain removes the directory.
var gatewayBuild struct {
	once sync.Once
	dir  string
	path string
	err  error
}

func TestMain(m *testing.M) {
	code := m.Run()
	if gatewayBuild.dir != "" {
		_ = os.RemoveAll(gatewayBuild.dir)
	}
	os.Exit(code)
}

// dockerCLI is a client of its own, for the tests to look at what the
// driver left on the daemon.
func dockerCLI(t *testing.T) *client.Client {
	t.Helper()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

// gatewayBinary is a static linux forjad for the daemon's architecture.
func gatewayBinary(t *testing.T) string {
	t.Helper()
	gatewayBuild.once.Do(func() {
		v, err := dockerCLI(t).ServerVersion(context.Background(), client.ServerVersionOptions{})
		if err != nil {
			gatewayBuild.err = err
			return
		}
		gatewayBuild.dir, gatewayBuild.err = os.MkdirTemp("", "forja-gateway-bin-")
		if gatewayBuild.err != nil {
			return
		}
		gatewayBuild.path = filepath.Join(gatewayBuild.dir, "forjad")
		build := exec.Command("go", "build", "-trimpath", "-o", gatewayBuild.path, "github.com/fuseone-io/fuseone-forja/cmd/forjad")
		build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+v.Arch, "CGO_ENABLED=0")
		if out, err := build.CombinedOutput(); err != nil {
			gatewayBuild.err = fmt.Errorf("go build forjad for linux/%s: %w\n%s", v.Arch, err, out)
		}
	})
	if gatewayBuild.err != nil {
		t.Fatalf("gateway binary: %v", gatewayBuild.err)
	}
	return gatewayBuild.path
}

// newDocker builds the driver and skips the test when no daemon answers:
// these are integration tests against the real Engine, never a mock. The
// gateway binary is built only once a daemon answered.
func newDocker(t *testing.T, allow ...string) *workspace.Docker {
	t.Helper()
	probe, err := workspace.NewDocker(workspace.Options{})
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := probe.Ping(ctx); err != nil {
		t.Skipf("no Docker daemon: %v", err)
	}
	d, err := workspace.NewDocker(workspace.Options{GatewayBinary: gatewayBinary(t), EgressAllow: allow})
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	return d
}

// newRunID is a fresh id, so parallel tests and leftovers of an earlier
// run never share a network name.
func newRunID(t *testing.T) string {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return "test-" + hex.EncodeToString(b[:])
}

// treeWith is a directory holding marker.txt and a nested file, the shape
// a clone has.
func treeWith(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "docs", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{"marker.txt": "forja was here\n", "docs/sub/c.md": "c\n"} {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// launch starts the workspace of runID from testImage with the tree and
// removes it when the test ends, whatever the test did.
func launch(t *testing.T, d *workspace.Docker, runID, tree string) *workspace.Container {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c, err := d.Launch(ctx, workspace.LaunchSpec{RunID: runID, Image: testImage, Tree: tree})
	if err != nil {
		t.Fatalf("Launch(%s): %v", testImage, err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = c.Close(ctx)
	})
	return c
}

// assertGone asserts the daemon no longer knows the container.
func assertGone(t *testing.T, id string) {
	t.Helper()
	_, err := dockerCLI(t).ContainerInspect(context.Background(), id, client.ContainerInspectOptions{})
	if !cerrdefs.IsNotFound(err) {
		t.Fatalf("ContainerInspect(%s) after Close = %v, want not found", id, err)
	}
}

func TestDocker_exec_exitCodeIsTheCommands(t *testing.T) {
	d := newDocker(t)
	c := launch(t, d, newRunID(t), treeWith(t))
	ctx := context.Background()

	res, err := c.Exec(ctx, "exit 7")
	if err != nil {
		t.Fatalf("Exec(exit 7): %v, want nil: a non-zero exit is a result", err)
	}
	if res.ExitCode != 7 {
		t.Fatalf("ExitCode = %d, want 7", res.ExitCode)
	}
	res, err = c.Exec(ctx, "echo out; echo err >&2")
	if err != nil {
		t.Fatalf("Exec(echo): %v", err)
	}
	if res.ExitCode != 0 || string(res.Stdout) != "out\n" || string(res.Stderr) != "err\n" {
		t.Fatalf("Exec(echo) = {%d %q %q}, want {0 \"out\\n\" \"err\\n\"}", res.ExitCode, res.Stdout, res.Stderr)
	}
}

// A gate may print without bound; the control plane keeps the last
// MaxStreamBytes of each stream and says so, whichever stream overflowed.
func TestDocker_exec_streamOverCap_tailKeptAndTruncated(t *testing.T) {
	d := newDocker(t)
	c := launch(t, d, newRunID(t), treeWith(t))
	ctx := context.Background()
	flood := "head -c 200000 /dev/zero | tr '\\0' x; printf TAIL"
	tests := []struct {
		name    string
		command string
		big     func(workspace.ExecResult) []byte
		small   func(workspace.ExecResult) []byte
	}{
		{name: "stdout overflows", command: flood + "; echo short >&2",
			big: func(r workspace.ExecResult) []byte { return r.Stdout }, small: func(r workspace.ExecResult) []byte { return r.Stderr }},
		{name: "stderr overflows", command: "echo short; { " + flood + "; } >&2",
			big: func(r workspace.ExecResult) []byte { return r.Stderr }, small: func(r workspace.ExecResult) []byte { return r.Stdout }},
	}
	for _, tt := range tests {
		res, err := c.Exec(ctx, tt.command)
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("%s: Exec = %v, exit %d", tt.name, err, res.ExitCode)
		}
		assertOverflow(t, tt.name, res, tt.big(res), tt.small(res))
	}
	res, err := c.Exec(ctx, "echo fits")
	if err != nil || res.Truncated || string(res.Stdout) != "fits\n" {
		t.Fatalf("Exec(echo fits) = {%q truncated=%v} err %v, want the whole stream and no truncation", res.Stdout, res.Truncated, err)
	}
}

// assertOverflow checks the stream that overflowed was cut to the cap
// ending in its last bytes, the other stream was left whole, and the
// result says it cut.
func assertOverflow(t *testing.T, name string, res workspace.ExecResult, big, small []byte) {
	t.Helper()
	if len(big) != workspace.MaxStreamBytes || !strings.HasSuffix(string(big), "TAIL") {
		t.Errorf("%s: big stream len %d, want %d ending in TAIL", name, len(big), workspace.MaxStreamBytes)
	}
	if string(small) != "short\n" {
		t.Errorf("%s: small stream = %q, want untouched", name, small)
	}
	if !res.Truncated {
		t.Errorf("%s: Truncated = false, want true", name)
	}
}

// The workspace's environment is the image's own plus the gateway's URL,
// which is an address, not a credential; nothing of the control plane's
// environment reaches it.
func TestDocker_env_imagesOwnPlusGatewayOnly(t *testing.T) {
	t.Setenv("FORJA_GITHUB_TOKEN", "secret-github-token-0123")
	t.Setenv("ANTHROPIC_API_KEY", "secret-anthropic-key-4567")
	d := newDocker(t)
	c := launch(t, d, newRunID(t), treeWith(t))
	ctx := context.Background()
	// A service's env is the sidecar's alone, never the tree's.
	withEnv := domain.Service{Image: sidecarImage, Env: map[string]string{"SIDECAR_ONLY_PASSWORD": "secret-sidecar-89ab"}}
	startReady(t, c, "cache", withEnv)

	res, err := c.Exec(ctx, "env")
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("Exec(env) = %v, exit %d", err, res.ExitCode)
	}
	env := string(res.Stdout)
	assertNoSecrets(t, "workspace", env)
	if !strings.Contains(env, "PATH=") {
		t.Errorf("container env has no PATH, the image's own env is missing:\n%s", env)
	}
	for _, want := range []string{
		"HTTP_PROXY=http://forja-gateway:3128", "HTTPS_PROXY=http://forja-gateway:3128",
		"http_proxy=http://forja-gateway:3128", "https_proxy=http://forja-gateway:3128",
		"NO_PROXY=localhost,127.0.0.1,::1", "no_proxy=localhost,127.0.0.1,::1",
	} {
		if !strings.Contains(env, want+"\n") {
			t.Errorf("container env lacks %s:\n%s", want, env)
		}
	}
	res, err = c.Exec(ctx, "cat marker.txt; cat docs/sub/c.md; pwd")
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("Exec(cat marker.txt) = %v, exit %d, stderr %q", err, res.ExitCode, res.Stderr)
	}
	if string(res.Stdout) != "forja was here\nc\n/workspace\n" {
		t.Fatalf("tree in the container = %q, want the copied files at /workspace", res.Stdout)
	}
}

// assertNoSecrets checks no control-plane secret, and no sidecar's own
// env, reached a container's environment.
func assertNoSecrets(t *testing.T, where, env string) {
	t.Helper()
	for _, leak := range []string{"FORJA_GITHUB_TOKEN", "secret-github-token", "ANTHROPIC_API_KEY", "secret-anthropic-key", "SIDECAR_ONLY_PASSWORD", "secret-sidecar"} {
		if strings.Contains(env, leak) {
			t.Errorf("%s env carries %q:\n%s", where, leak, env)
		}
	}
}

func TestDocker_ctxDeadline_duringExec_containerRemoved(t *testing.T) {
	d := newDocker(t)
	c := launch(t, d, newRunID(t), treeWith(t))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	start := time.Now()
	res, err := c.Exec(ctx, "echo started; sleep 60")
	if err == nil {
		t.Fatalf("Exec(sleep 60) under a 1s deadline = nil error, exit %d", res.ExitCode)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("Exec returned after %v, want soon after the deadline", elapsed)
	}
	if string(res.Stdout) != "started\n" {
		t.Errorf("partial stdout = %q, want what the command printed before the deadline", res.Stdout)
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer closeCancel()
	if err := c.Close(closeCtx); err != nil {
		t.Fatalf("Close after the deadline: %v", err)
	}
	assertGone(t, c.ID())
}

func rawClient(t *testing.T) *client.Client {
	t.Helper()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	return cli
}

// Each launch gets a network of its own, labelled as Forja's, and the
// workspace sits on it alone: not on the default bridge every container
// shares, and not on another run's.
func TestDocker_launch_workspaceOnItsOwnNetworkOnly(t *testing.T) {
	d := newDocker(t)
	a, b := launch(t, d, newRunID(t), treeWith(t)), launch(t, d, newRunID(t), treeWith(t))

	if a.NetworkID() == "" || a.NetworkID() == b.NetworkID() {
		t.Fatalf("network ids = %q and %q, want two distinct networks", a.NetworkID(), b.NetworkID())
	}
	cli := rawClient(t)
	inspect, err := cli.ContainerInspect(context.Background(), a.ID(), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatalf("ContainerInspect: %v", err)
	}
	var on []string
	for _, ep := range inspect.Container.NetworkSettings.Networks {
		on = append(on, ep.NetworkID)
	}
	if len(on) != 1 || on[0] != a.NetworkID() {
		t.Fatalf("workspace on networks %q, want only %q", on, a.NetworkID())
	}
	net, err := cli.NetworkInspect(context.Background(), a.NetworkID(), client.NetworkInspectOptions{})
	if err != nil {
		t.Fatalf("NetworkInspect: %v", err)
	}
	if _, ok := net.Network.Labels["forja.run"]; !ok || !strings.HasPrefix(net.Network.Name, "forja-run-") {
		t.Fatalf("network = {name %q, labels %v}, want forja-run-* labelled forja.run", net.Network.Name, net.Network.Labels)
	}
}

// assertNetworkGone asserts the daemon no longer knows the network.
func assertNetworkGone(t *testing.T, id string) {
	t.Helper()
	_, err := rawClient(t).NetworkInspect(context.Background(), id, client.NetworkInspectOptions{})
	if !cerrdefs.IsNotFound(err) {
		t.Fatalf("NetworkInspect(%s) after Close = %v, want not found", id, err)
	}
}

func TestDocker_launch_imageUnknown_errorNamesIt(t *testing.T) {
	d := newDocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	_, err := d.Launch(ctx, workspace.LaunchSpec{RunID: newRunID(t), Image: "forja-no-such-image-0000:none", Tree: treeWith(t)})
	if err == nil || !strings.Contains(err.Error(), "forja-no-such-image-0000") {
		t.Fatalf("Launch(unknown image) = %v, want an error naming the image", err)
	}
}
