package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"github.com/fuseone-io/fuseone-forja/internal/workspace"
)

// testImage is small and ships a POSIX sh, which is all Exec needs.
const testImage = "alpine:3.20"

// newDocker builds the driver and skips the test when no daemon answers:
// these are integration tests against the real Engine, never a mock.
func newDocker(t *testing.T) *workspace.Docker {
	t.Helper()
	d, err := workspace.NewDocker()
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.Ping(ctx); err != nil {
		t.Skipf("no Docker daemon: %v", err)
	}
	return d
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

// launch starts a container from testImage with the tree and removes it
// when the test ends, whatever the test did.
func launch(t *testing.T, d *workspace.Docker, tree string) *workspace.Container {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c, err := d.Launch(ctx, testImage, tree)
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
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	_, err = cli.ContainerInspect(context.Background(), id, client.ContainerInspectOptions{})
	if !cerrdefs.IsNotFound(err) {
		t.Fatalf("ContainerInspect(%s) after Close = %v, want not found", id, err)
	}
}

func TestDocker_exec_exitCodeIsTheCommands(t *testing.T) {
	d := newDocker(t)
	c := launch(t, d, treeWith(t))
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
	c := launch(t, d, treeWith(t))
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
		big, small := tt.big(res), tt.small(res)
		if len(big) != workspace.MaxStreamBytes || !strings.HasSuffix(string(big), "TAIL") {
			t.Errorf("%s: big stream len %d, want %d ending in TAIL", tt.name, len(big), workspace.MaxStreamBytes)
		}
		if string(small) != "short\n" {
			t.Errorf("%s: small stream = %q, want untouched", tt.name, small)
		}
		if !res.Truncated {
			t.Errorf("%s: Truncated = false, want true", tt.name)
		}
	}
	res, err := c.Exec(ctx, "echo fits")
	if err != nil || res.Truncated || string(res.Stdout) != "fits\n" {
		t.Fatalf("Exec(echo fits) = {%q truncated=%v} err %v, want the whole stream and no truncation", res.Stdout, res.Truncated, err)
	}
}

func TestDocker_env_isTheImagesOnly(t *testing.T) {
	t.Setenv("FORJA_GITHUB_TOKEN", "secret-github-token-0123")
	t.Setenv("ANTHROPIC_API_KEY", "secret-anthropic-key-4567")
	d := newDocker(t)
	c := launch(t, d, treeWith(t))
	ctx := context.Background()

	res, err := c.Exec(ctx, "env")
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("Exec(env) = %v, exit %d", err, res.ExitCode)
	}
	env := string(res.Stdout)
	for _, leak := range []string{"FORJA_GITHUB_TOKEN", "secret-github-token", "ANTHROPIC_API_KEY", "secret-anthropic-key"} {
		if strings.Contains(env, leak) {
			t.Errorf("container env carries %q:\n%s", leak, env)
		}
	}
	if !strings.Contains(env, "PATH=") {
		t.Errorf("container env has no PATH, the image's own env is missing:\n%s", env)
	}
	res, err = c.Exec(ctx, "cat marker.txt; cat docs/sub/c.md; pwd")
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("Exec(cat marker.txt) = %v, exit %d, stderr %q", err, res.ExitCode, res.Stderr)
	}
	if string(res.Stdout) != "forja was here\nc\n/workspace\n" {
		t.Fatalf("tree in the container = %q, want the copied files at /workspace", res.Stdout)
	}
}

func TestDocker_close_removesContainer(t *testing.T) {
	d := newDocker(t)
	c := launch(t, d, treeWith(t))
	ctx := context.Background()

	if err := c.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertGone(t, c.ID())
	if err := c.Close(ctx); err != nil {
		t.Fatalf("second Close: %v, want nil: Close is idempotent", err)
	}
	if _, err := c.Exec(ctx, "true"); err == nil {
		t.Fatal("Exec after Close = nil error, want a failure")
	}
}

func TestDocker_ctxDeadline_duringExec_containerRemoved(t *testing.T) {
	d := newDocker(t)
	c := launch(t, d, treeWith(t))
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

// labelledContainers counts the containers this package marks as its own,
// running or not.
func labelledContainers(t *testing.T) int {
	t.Helper()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	list, err := cli.ContainerList(context.Background(), client.ContainerListOptions{
		All:     true,
		Filters: client.Filters{}.Add("label", "forja.run=doctor"),
	})
	if err != nil {
		t.Fatalf("ContainerList: %v", err)
	}
	return len(list.Items)
}

func TestDocker_launch_treeMissing_errorAndNoContainerLeft(t *testing.T) {
	d := newDocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	before := labelledContainers(t)

	_, err := d.Launch(ctx, testImage, filepath.Join(t.TempDir(), "no-such-tree"))
	if err == nil {
		t.Fatal("Launch(missing tree) = nil error, want a copy failure")
	}
	if after := labelledContainers(t); after != before {
		t.Fatalf("labelled containers went from %d to %d: the container of a failed launch was left behind", before, after)
	}
}

func TestDocker_launch_imageUnknown_errorNamesIt(t *testing.T) {
	d := newDocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	_, err := d.Launch(ctx, "forja-no-such-image-0000:none", treeWith(t))
	if err == nil || !strings.Contains(err.Error(), "forja-no-such-image-0000") {
		t.Fatalf("Launch(unknown image) = %v, want an error naming the image", err)
	}
}
