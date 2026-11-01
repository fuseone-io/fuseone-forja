// Package workspace is the Docker implementation of the ephemeral tree the
// gates run in. It speaks to the Engine API through the official client
// module rather than driving the docker CLI, because the CLI folds the
// command's exit code and docker's own failures into one process status:
// `docker exec` exits 1 both for a gate that failed and for a daemon that
// went away. The API gives the exec's ExitCode from ExecInspect, apart from
// any transport error, which is the distinction a gate's verdict rests on.
package workspace

import (
	"context"
	"fmt"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// workDir is where the tree is copied and every command runs.
const workDir = "/workspace"

// labelRun marks every container this package creates, so a stray one can
// be told from the operator's own.
const labelRun = "forja.run"

// Docker launches containers on the daemon the environment points at.
type Docker struct {
	cli *client.Client
}

// NewDocker builds the client from the environment (DOCKER_HOST and the
// variables next to it). Nothing is contacted until the first call.
func NewDocker() (*Docker, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("workspace: docker client: %w", err)
	}
	return &Docker{cli: cli}, nil
}

// Ping checks the daemon answers.
func (d *Docker) Ping(ctx context.Context) error {
	if _, err := d.cli.Ping(ctx, client.PingOptions{}); err != nil {
		return fmt.Errorf("workspace: ping docker: %w", err)
	}
	return nil
}

// Launch pulls image when the daemon lacks it, starts a container that
// idles at workDir with the image's own environment and nothing of the
// control plane's, and copies tree into workDir. The tree is copied, not
// mounted: the daemon may run in a VM that does not share this filesystem.
// A failure after the container exists removes it.
func (d *Docker) Launch(ctx context.Context, image, tree string) (*Container, error) {
	if err := d.ensureImage(ctx, image); err != nil {
		return nil, fmt.Errorf("workspace: image %s: %w", image, err)
	}
	created, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:      image,
			Cmd:        []string{"sleep", "infinity"},
			WorkingDir: workDir,
			Env:        nil,
			Labels:     map[string]string{labelRun: "doctor"},
		},
		HostConfig: &container.HostConfig{},
	})
	if err != nil {
		return nil, fmt.Errorf("workspace: create container from %s: %w", image, err)
	}
	c := &Container{cli: d.cli, id: created.ID}
	if err := c.startWithTree(ctx, tree); err != nil {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
		defer cancel()
		// The launch failure is the error; a container that could not be
		// removed on top of it adds nothing the caller can act on.
		_ = c.Close(closeCtx)
		return nil, err
	}
	return c, nil
}

// ensureImage pulls image only when the daemon does not already hold it.
func (d *Docker) ensureImage(ctx context.Context, image string) error {
	_, err := d.cli.ImageInspect(ctx, image)
	if err == nil {
		return nil
	}
	if !cerrdefs.IsNotFound(err) {
		return err
	}
	pull, err := d.cli.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("pull: %w", err)
	}
	if err := pull.Wait(ctx); err != nil {
		return fmt.Errorf("pull: %w", err)
	}
	return nil
}

func (c *Container) startWithTree(ctx context.Context, tree string) error {
	if _, err := c.cli.ContainerStart(ctx, c.id, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("workspace: start container: %w", err)
	}
	if err := copyTree(ctx, c.cli, c.id, tree); err != nil {
		return fmt.Errorf("workspace: copy tree into container: %w", err)
	}
	return nil
}
