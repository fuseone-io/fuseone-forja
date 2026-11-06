package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	dockercontainer "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/fuseone-io/fuseone-forja/internal/workspace"
)

// startWith is h's start, its store released, and what it wrote.
func startWith(t *testing.T, h host) (string, error) {
	t.Helper()
	var stderr bytes.Buffer
	_, closeRuns, err := h.start(context.Background(), &stderr)
	if err == nil {
		closeRuns()
	}
	return stderr.String(), err
}

// The start sweeps the daemon for the state directory's instance and
// reports how many objects of which runs it removed.
func TestHostStart_sweep_ownInstanceCountAndRunsReported(t *testing.T) {
	t.Parallel()
	env := envOf(map[string]string{EnvStateDir: t.TempDir(), EnvWorkspaceRuntime: "runc"})
	var asked string
	sweep := func(_ context.Context, owner string) (workspace.Swept, error) {
		asked = owner
		return workspace.Swept{Containers: 3, Networks: 2, Runs: []string{"run-a", "run-b"}}, nil
	}

	stderr, err := startWith(t, host{getenv: env, sweep: sweep})

	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if want, _ := instanceOwner(env); asked == "" || asked != want {
		t.Errorf("swept for %q, want the state directory's instance %q", asked, want)
	}
	if !strings.Contains(stderr, "removed 3 containers and 2 networks left by dead runs: run-a, run-b\n") {
		t.Errorf("stderr = %q, want the counts and the run ids", stderr)
	}
}

// A sweep that fails, at once or part way, or a daemon that never
// answers, does not stop the start: the cause and what was removed go to
// stderr.
func TestHostStart_sweepFails_startsAndReportsTheCause(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		sweep func(context.Context, string) (workspace.Swept, error)
		want  []string
	}{
		"daemon down": {
			sweep: func(context.Context, string) (workspace.Swept, error) {
				return workspace.Swept{}, errors.New("cannot connect to the docker daemon")
			},
			want: []string{"cannot connect to the docker daemon"},
		},
		"part way": {
			sweep: func(context.Context, string) (workspace.Swept, error) {
				return workspace.Swept{Containers: 1, Runs: []string{"run-a"}}, errors.New("remove network: in use")
			},
			want: []string{"removed 1 containers and 0 networks left by dead runs: run-a", "remove network: in use"},
		},
		"never answers": {
			sweep: func(ctx context.Context, _ string) (workspace.Swept, error) {
				<-ctx.Done()
				return workspace.Swept{}, ctx.Err()
			},
			want: []string{context.DeadlineExceeded.Error()},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			env := envOf(map[string]string{EnvStateDir: t.TempDir(), EnvWorkspaceRuntime: "runc"})
			began := time.Now()

			stderr, err := startWith(t, host{getenv: env, sweep: tt.sweep, startup: 500 * time.Millisecond})

			if err != nil || time.Since(began) > 20*time.Second {
				t.Fatalf("start = %v after %v, want it to serve within the startup deadline", err, time.Since(began))
			}
			for _, w := range tt.want {
				if !strings.Contains(stderr, w) {
					t.Errorf("stderr = %q, want it to report %q", stderr, w)
				}
			}
		})
	}
}

// With no state directory there is no instance, and nothing is swept.
func TestHostStart_noStateDir_noSweep(t *testing.T) {
	t.Parallel()
	swept := false
	sweep := func(context.Context, string) (workspace.Swept, error) {
		swept = true
		return workspace.Swept{}, nil
	}
	if _, err := startWith(t, host{getenv: quietEnv, sweep: sweep}); err != nil || swept {
		t.Fatalf("start = %v, swept %v; want it to start without a sweep", err, swept)
	}
}

// A start the operator already stopped sends no sweep and reports
// nothing about one.
func TestHostStart_stoppedBeforeTheSweep_noSweepNothingReported(t *testing.T) {
	t.Parallel()
	swept := false
	sweep := func(context.Context, string) (workspace.Swept, error) {
		swept = true
		return workspace.Swept{}, errors.New("swept")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stderr bytes.Buffer
	env := envOf(map[string]string{EnvStateDir: t.TempDir(), EnvWorkspaceRuntime: "runc"})

	_, closeRuns, err := host{getenv: env, sweep: sweep}.start(ctx, &stderr)

	if err == nil {
		closeRuns()
	}
	if swept || stderr.Len() != 0 {
		t.Fatalf("swept %v, stderr %q; want no sweep and nothing reported", swept, stderr.String())
	}
}

// plantObjects leaves on the daemon what a run that died mid-create
// leaves: its internal network and a container created on it and never
// started, whose config holds secret in its environment and its command.
func plantObjects(t *testing.T, cli *client.Client, labels map[string]string, secret string) (containerID, networkID string) {
	t.Helper()
	ctx := context.Background()
	name := "forja-run-" + labels["forja.run"]
	net, err := cli.NetworkCreate(ctx, name, client.NetworkCreateOptions{Driver: "bridge", Internal: true, Labels: labels})
	if err != nil {
		t.Fatalf("NetworkCreate: %v", err)
	}
	t.Cleanup(func() { _, _ = cli.NetworkRemove(context.Background(), net.ID, client.NetworkRemoveOptions{}) })
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name + "-db",
		Config: &dockercontainer.Config{
			Image: "alpine:3.20", Cmd: []string{"echo", secret}, Env: []string{"POSTGRES_PASSWORD=" + secret}, Labels: labels,
		},
		HostConfig: &dockercontainer.HostConfig{NetworkMode: dockercontainer.NetworkMode(name)},
	})
	if err != nil {
		t.Fatalf("ContainerCreate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = cli.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	})
	return created.ID, net.ID
}

// dockerForStart is a client of the daemon the Docker tests drive, with
// the image the planted objects are created from.
func dockerForStart(t *testing.T) *client.Client {
	t.Helper()
	if os.Getenv("FORJA_DOCKER_TEST") != "1" {
		t.Skip("set FORJA_DOCKER_TEST=1 (make docker-test) to run the tests against the Docker Engine")
	}
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if _, err := cli.ImageInspect(ctx, "alpine:3.20"); err != nil {
		pull, err := cli.ImagePull(ctx, "alpine:3.20", client.ImagePullOptions{})
		if err != nil {
			t.Fatalf("ImagePull: %v", err)
		}
		if err := pull.Wait(ctx); err != nil {
			t.Fatalf("ImagePull: %v", err)
		}
	}
	return cli
}

func randomID(t *testing.T) string {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}

// What a dead run of this instance left on the daemon, a container created
// and never started and the run's network, is gone once the server
// started; another instance's are not. The report names the run, never
// what the container was configured with.
func TestHostStart_deadRunLeftObjects_removedByTheStart(t *testing.T) {
	t.Parallel()
	cli := dockerForStart(t)
	env := envOf(map[string]string{EnvStateDir: t.TempDir(), EnvWorkspaceRuntime: "runc"})
	owner, err := instanceOwner(env)
	if err != nil {
		t.Fatalf("instanceOwner: %v", err)
	}
	dead, theirs, secret := "start-test-"+randomID(t), "start-test-"+randomID(t), "secret-"+randomID(t)
	deadContainer, deadNetwork := plantObjects(t, cli, map[string]string{"forja.run": dead, "forja.owner": owner}, secret)
	theirContainer, theirNetwork := plantObjects(t, cli, map[string]string{"forja.run": theirs, "forja.owner": randomID(t)}, secret)

	stderr, err := startWith(t, host{getenv: env})

	if err != nil {
		t.Fatalf("start: %v", err)
	}
	ctx := context.Background()
	if _, err := cli.ContainerInspect(ctx, deadContainer, client.ContainerInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Errorf("the dead run's container after the start: %v, want it gone", err)
	}
	if _, err := cli.NetworkInspect(ctx, deadNetwork, client.NetworkInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Errorf("the dead run's network after the start: %v, want it gone", err)
	}
	if _, err := cli.ContainerInspect(ctx, theirContainer, client.ContainerInspectOptions{}); err != nil {
		t.Errorf("another instance's container after the start: %v, want it kept", err)
	}
	if _, err := cli.NetworkInspect(ctx, theirNetwork, client.NetworkInspectOptions{}); err != nil {
		t.Errorf("another instance's network after the start: %v, want it kept", err)
	}
	if !strings.Contains(stderr, "1 containers and 1 networks left by dead runs: "+dead+"\n") || strings.Contains(stderr, secret) {
		t.Errorf("stderr = %q, want the counts and %s, and nothing of the container's config", stderr, dead)
	}
}
