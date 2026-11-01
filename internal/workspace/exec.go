package workspace

import (
	"context"
	"fmt"
	"io"
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

// Container is one running workspace.
type Container struct {
	cli *client.Client
	id  string
}

// ID is the daemon's identifier of the container.
func (c *Container) ID() string { return c.id }

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

// Close kills and removes the container. A container the daemon no longer
// knows is already closed.
func (c *Container) Close(ctx context.Context) error {
	_, err := c.cli.ContainerRemove(ctx, c.id, client.ContainerRemoveOptions{Force: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("workspace: remove container: %w", err)
	}
	return nil
}
