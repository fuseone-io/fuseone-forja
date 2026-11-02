// Package workspace is the Docker implementation of the ephemeral tree the
// gates run in. It speaks to the Engine API through the official client
// module rather than driving the docker CLI, because the CLI folds the
// command's exit code and docker's own failures into one process status:
// `docker exec` exits 1 both for a gate that failed and for a daemon that
// went away. The API gives the exec's ExitCode from ExecInspect, apart from
// any transport error, which is the distinction a gate's verdict rests on.
//
// Each run gets its own internal network. The workspace and its sidecars
// are attached to it alone; the run's egress gateway is the only container
// there that may also be attached to a network with a route out.
package workspace

import (
	"context"
	"fmt"
	"os"
	"runtime"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// workDir is where the tree is copied and every command runs.
const workDir = "/workspace"

// labelRun marks every container and network this package creates with
// the id of its run, so a stray one can be told from the operator's own
// and traced to its run.
const labelRun = "forja.run"

// noProxy keeps the workspace's own loopback off the gateway.
const noProxy = "localhost,127.0.0.1,::1"

// Options configures the driver.
type Options struct {
	// GatewayBinary is a static linux forjad for the daemon's architecture,
	// copied into every run's gateway. Empty means this process's own
	// executable on linux; elsewhere a launch is refused.
	GatewayBinary string
	// EgressAllow is the host:port list a gateway tunnels to, already
	// validated. Empty, the gateway is not attached to any network with a
	// route out and nothing leaves.
	EgressAllow []string
	// AgentBinary is a linux agent for the daemon's architecture, installed
	// in every agent workspace. Empty, LaunchAgent is refused.
	AgentBinary string
	// Model configures the model proxy of every agent launch.
	Model ModelOptions
}

// Docker launches containers on the daemon the environment points at.
type Docker struct {
	cli  *client.Client
	opts Options
}

// NewDocker builds the client from the environment (DOCKER_HOST and the
// variables next to it). Nothing is contacted until the first call.
func NewDocker(opts Options) (*Docker, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("workspace: docker client: %w", err)
	}
	return &Docker{cli: cli, opts: opts}, nil
}

// Ping checks the daemon answers.
func (d *Docker) Ping(ctx context.Context) error {
	if _, err := d.cli.Ping(ctx, client.PingOptions{}); err != nil {
		return fmt.Errorf("workspace: ping docker: %w", err)
	}
	return nil
}

// Launch creates run runID's internal network and its gateway, then starts
// a container from image that idles at workDir on that network alone, with
// the image's own environment plus the gateway's address as its proxy and
// nothing of the control plane's, and copies tree into workDir. The tree is
// copied, not mounted: the daemon may run in a VM that does not share this
// filesystem. Launch returns once the gateway said it is ready; any failure
// removes whatever it had created.
func (d *Docker) Launch(ctx context.Context, runID, image, tree string) (*Container, error) {
	return d.launch(ctx, launchSpec{runID: runID, image: image, tree: tree})
}

// launchSpec is one launch: plain, or an agent's when agent is set.
type launchSpec struct {
	runID, image, tree string
	agent              *agentSpec
}

func (d *Docker) launch(ctx context.Context, spec launchSpec) (*Container, error) {
	if err := validRunID(spec.runID); err != nil {
		return nil, err
	}
	binary, err := resolveGatewayBinary(d.opts.GatewayBinary, runtime.GOOS, os.Executable)
	if err != nil {
		return nil, err
	}
	for _, img := range []string{spec.image, gatewayImage} {
		if err := ensureImage(ctx, d.cli, img); err != nil {
			return nil, fmt.Errorf("workspace: image %s: %w", img, err)
		}
	}
	c := &Container{cli: d.cli, runID: spec.runID}
	if err := d.assemble(ctx, c, binary, spec); err != nil {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
		defer cancel()
		// The launch failure is the error; what could not be removed on top
		// of it adds nothing the caller can act on.
		_ = c.Close(closeCtx)
		return nil, err
	}
	return c, nil
}

// assemble builds the run's pieces in order, recording each in c as soon
// as it exists so that Close removes it whatever fails next. An agent
// launch adds the model proxy before the workspace, whose environment
// names it, and installs the agent once the tree is in.
func (d *Docker) assemble(ctx context.Context, c *Container, binary string, spec launchSpec) error {
	var err error
	if c.network, err = createNetwork(ctx, d.cli, c.runID); err != nil {
		return err
	}
	if c.gateway, err = createGateway(ctx, d.cli, c.runID, d.opts.EgressAllow); err != nil {
		return err
	}
	if err := startGateway(ctx, d.cli, gatewaySpec{id: c.gateway, runID: c.runID, binary: binary, allow: d.opts.EgressAllow}); err != nil {
		return err
	}
	env := proxyEnv(noProxy)
	if spec.agent != nil {
		if err := d.addModel(ctx, c, binary, spec.agent); err != nil {
			return err
		}
		env = spec.agent.env()
	}
	if c.id, err = d.createWorkspace(ctx, c.runID, spec.image, env); err != nil {
		return err
	}
	if err := c.startWithTree(ctx, spec.tree); err != nil {
		return err
	}
	if spec.agent != nil {
		return c.installAgent(ctx, spec.agent.binary)
	}
	return nil
}

// createWorkspace creates the workspace container on the run's network
// alone, never on the default bridge. The network is set at creation: a
// container connected afterwards does not get the embedded DNS.
func (d *Docker) createWorkspace(ctx context.Context, runID, image string, env []string) (string, error) {
	netName := networkName(runID)
	created, err := d.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: netName + "-workspace",
		Config: &container.Config{
			Image:      image,
			Cmd:        []string{"sleep", "infinity"},
			WorkingDir: workDir,
			Env:        env,
			Labels:     map[string]string{labelRun: runID},
		},
		HostConfig:       &container.HostConfig{NetworkMode: container.NetworkMode(netName)},
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{netName: {}}},
	})
	if err != nil {
		return "", fmt.Errorf("workspace: create container from %s: %w", image, err)
	}
	return created.ID, nil
}

// proxyEnv points both spellings tools read at the gateway, except for the
// hosts in skip. A URL is an address, not a credential.
func proxyEnv(skip string) []string {
	return []string{
		"HTTP_PROXY=" + gatewayURL, "HTTPS_PROXY=" + gatewayURL,
		"http_proxy=" + gatewayURL, "https_proxy=" + gatewayURL,
		"NO_PROXY=" + skip, "no_proxy=" + skip,
	}
}

// ensureImage pulls image only when the daemon does not already hold it.
func ensureImage(ctx context.Context, cli *client.Client, image string) error {
	_, err := cli.ImageInspect(ctx, image)
	if err == nil {
		return nil
	}
	if !cerrdefs.IsNotFound(err) {
		return err
	}
	pull, err := cli.ImagePull(ctx, image, client.ImagePullOptions{})
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
