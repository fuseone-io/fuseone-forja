package workspace

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

// closeTimeout bounds the removal of a container whose caller's context
// may already be dead.
const closeTimeout = 30 * time.Second

// inspectRetry is the pause between two reads of an exec that the daemon
// still reports running after its streams closed.
const inspectRetry = 10 * time.Millisecond

// ExecResult is what one command left behind, raw: the last MaxStreamBytes
// of each stream, and whether either had more.
type ExecResult struct {
	ExitCode  int
	Stdout    []byte
	Stderr    []byte
	Truncated bool
}

// Container is one running workspace, with the network, the gateway and
// the registry proxy of its run, the model proxy of an agent launch, and
// the sidecars started for it.
type Container struct {
	cli     *client.Client
	runID   string
	id      string
	gateway string
	// model is empty for a launch that is not an agent's.
	model string
	// registry is empty for a run that declares no registry.
	registry string
	network  string
	// runtime is the daemon runtime every container of the run is created
	// under; empty is the daemon's default.
	runtime string
	// owner is the instance every object of the run is labelled with;
	// empty labels none.
	owner string

	mu       sync.Mutex
	sidecars []string
}

func (c *Container) track(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sidecars = append(c.sidecars, id)
}

func (c *Container) tracked() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.sidecars...)
}

// ID is the daemon's identifier of the container.
func (c *Container) ID() string { return c.id }

// NetworkID is the daemon's identifier of the run's network.
func (c *Container) NetworkID() string { return c.network }

// Exec runs command with sh -c at workDir and returns its exit code and
// streams. A non-zero exit is a result. The error is for a failure to run
// at all, including ctx ending first; the result then carries the bytes
// read so far and ExitCode -1.
func (c *Container) Exec(ctx context.Context, command string) (ExecResult, error) {
	res := ExecResult{ExitCode: -1}
	created, err := c.cli.ExecCreate(ctx, c.id, client.ExecCreateOptions{
		Cmd:          []string{"sh", "-c", command},
		WorkingDir:   workDir,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return res, fmt.Errorf("workspace: exec create: %w", err)
	}
	attached, err := c.cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return res, fmt.Errorf("workspace: exec attach: %w", err)
	}
	stdout, stderr := newTail(MaxStreamBytes), newTail(MaxStreamBytes)
	copyErr := drain(ctx, stdout, stderr, &attached.HijackedResponse)
	res.Stdout, res.Stderr = stdout.Bytes(), stderr.Bytes()
	res.Truncated = stdout.Truncated() || stderr.Truncated()
	if ctx.Err() != nil {
		return res, fmt.Errorf("workspace: exec: %w", ctx.Err())
	}
	if copyErr != nil {
		return res, fmt.Errorf("workspace: exec stream: %w", copyErr)
	}
	res.ExitCode, err = c.exitCode(ctx, created.ID)
	if err != nil {
		return res, err
	}
	return res, nil
}

// drain demultiplexes the exec's streams until they close. When ctx ends
// first the connection is closed, which unblocks the copy with whatever
// arrived; the process inside the container is left to Close.
func drain(ctx context.Context, stdout, stderr io.Writer, hijacked *client.HijackedResponse) error {
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
		}
		hijacked.Close()
	}()
	_, err := stdcopy.StdCopy(stdout, stderr, hijacked.Reader)
	return err
}

// exitCode reads the exec's exit code once the daemon stops reporting it
// as running, which can lag the streams closing by a moment.
func (c *Container) exitCode(ctx context.Context, execID string) (int, error) {
	for {
		inspect, err := c.cli.ExecInspect(ctx, execID, client.ExecInspectOptions{})
		if err != nil {
			return -1, fmt.Errorf("workspace: exec inspect: %w", err)
		}
		if !inspect.Running {
			return inspect.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			return -1, fmt.Errorf("workspace: exec inspect: %w", ctx.Err())
		case <-time.After(inspectRetry):
		}
	}
}

// Close kills and removes every sidecar, then the workspace, then its
// model proxy, which takes the run token with it, its registry proxy and
// its gateway, each with the anonymous volumes its image declared, then the
// run's network, which the daemon removes only once nothing is attached to
// it. A container or network the daemon no longer knows is already closed,
// so Close is idempotent. Every removal is attempted whatever the one
// before it returned.
func (c *Container) Close(ctx context.Context) error {
	sidecars := c.tracked()
	errs := make([]error, 0, len(sidecars)+5)
	for _, id := range sidecars {
		errs = append(errs, removeContainer(ctx, c.cli, id))
	}
	errs = append(errs, removeContainer(ctx, c.cli, c.id))
	if err := removeContainer(ctx, c.cli, c.model); err != nil {
		errs = append(errs, fmt.Errorf("model: %w", err))
	}
	if err := removeContainer(ctx, c.cli, c.registry); err != nil {
		errs = append(errs, fmt.Errorf("registry proxy: %w", err))
	}
	if err := removeContainer(ctx, c.cli, c.gateway); err != nil {
		errs = append(errs, fmt.Errorf("gateway: %w", err))
	}
	errs = append(errs, removeNetwork(ctx, c.cli, c.network))
	return errors.Join(errs...)
}

// removeContainer kills and removes one container and its anonymous
// volumes; one that was never created or that the daemon no longer knows
// is already removed.
func removeContainer(ctx context.Context, cli *client.Client, id string) error {
	if id == "" {
		return nil
	}
	_, err := cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("workspace: remove container %s: %w", id, err)
	}
	return nil
}
