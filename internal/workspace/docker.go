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

	"github.com/fuseone-io/fuseone-forja/internal/proxy/registry"
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
	// RegistryCredentials holds the operator's read credential of each
	// registry entry. A run receives, in its proxy alone, those of the
	// entries its contract declares; a declared entry with none refuses the
	// launch.
	RegistryCredentials map[string]registry.Credential
	// Runtime is the daemon runtime every container of a run is created
	// under: the workspace, its sidecars and its proxies. Empty means the
	// daemon's default; a name the daemon does not list refuses the launch.
	Runtime string
	// AgentBinary is a linux agent for the daemon's architecture, installed
	// in every agent workspace. Empty, LaunchAgent is refused.
	AgentBinary string
	// Model configures the model proxy of every agent launch.
	Model ModelOptions
	// Owner is the instance every object of a run is labelled as created
	// by, and the one Sweep removes the objects of. Empty, objects carry no
	// owner and Sweep is refused.
	Owner string
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

// Close releases the client's connections to the daemon; what it launched
// stays on the daemon.
func (d *Docker) Close() error { return d.cli.Close() }

// LaunchSpec is one workspace to create: run RunID's, from Image, with a
// copy of Tree, a directory on the control plane, at workDir. Registries
// are the contract's private registries, reached through the run's
// registry proxy.
type LaunchSpec struct {
	RunID      string
	Image      string
	Tree       string
	Registries []string
}

// Launch creates the run's internal network and its gateway, and its
// registry proxy when the spec declares registries, then starts a
// container from the image that idles at workDir on that network alone,
// with the image's own environment plus the proxies' addresses and nothing
// of the control plane's, and copies the tree into workDir. The tree is
// copied, not mounted: the daemon may run in a VM that does not share this
// filesystem. Launch returns once every proxy said it is ready; any
// failure removes whatever it had created.
func (d *Docker) Launch(ctx context.Context, spec LaunchSpec) (*Container, error) {
	return d.launch(ctx, spec, nil)
}

// launch is Launch for a plain spec, or an agent's when agent is set.
func (d *Docker) launch(ctx context.Context, spec LaunchSpec, agent *agentSpec) (*Container, error) {
	if err := validRunID(spec.RunID); err != nil {
		return nil, err
	}
	creds, err := credentialsFor(d.opts.RegistryCredentials, spec.Registries)
	if err != nil {
		return nil, err
	}
	if err := checkRuntime(ctx, d.cli, d.opts.Runtime); err != nil {
		return nil, err
	}
	binary, err := resolveGatewayBinary(d.opts.GatewayBinary, runtime.GOOS, os.Executable)
	if err != nil {
		return nil, err
	}
	for _, img := range []string{spec.Image, gatewayImage} {
		if err := ensureImage(ctx, d.cli, img); err != nil {
			return nil, fmt.Errorf("workspace: image %s: %w", img, err)
		}
	}
	c := &Container{cli: d.cli, runID: spec.RunID, runtime: d.opts.Runtime, owner: d.opts.Owner}
	if err := d.assemble(ctx, c, launchPlan{spec: spec, binary: binary, creds: creds, agent: agent}); err != nil {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
		defer cancel()
		// The launch failure is the error; what could not be removed on top
		// of it adds nothing the caller can act on.
		_ = c.Close(closeCtx)
		return nil, err
	}
	return c, nil
}

// launchPlan is a spec checked by launch: the binary every proxy runs, the
// credentials of the spec's registries, and the agent of an agent launch.
type launchPlan struct {
	spec   LaunchSpec
	binary string
	creds  map[string]registry.Credential
	agent  *agentSpec
}

// assemble builds the run's pieces in order, recording each in c as soon
// as it exists so that Close removes it whatever fails next. The proxies
// come before the workspace, which is created with its final environment;
// an agent launch installs the agent once the tree is in.
func (d *Docker) assemble(ctx context.Context, c *Container, plan launchPlan) error {
	var err error
	if c.network, err = c.createNetwork(ctx); err != nil {
		return err
	}
	if c.gateway, err = c.createGateway(ctx, d.opts.EgressAllow); err != nil {
		return err
	}
	if err := startGateway(ctx, d.cli, gatewaySpec{id: c.gateway, runID: c.runID, binary: plan.binary, allow: d.opts.EgressAllow}); err != nil {
		return err
	}
	env, err := d.addProxies(ctx, c, plan)
	if err != nil {
		return err
	}
	if c.id, err = c.createWorkspace(ctx, plan.spec.Image, env); err != nil {
		return err
	}
	if err := c.startWithTree(ctx, plan.spec.Tree); err != nil {
		return err
	}
	if plan.agent != nil {
		return c.installAgent(ctx, plan.agent.binary)
	}
	return nil
}

// addProxies starts the run's registry proxy when the spec declares
// registries and its model proxy on an agent launch, and returns the
// workspace's environment, which names them.
func (d *Docker) addProxies(ctx context.Context, c *Container, plan launchPlan) ([]string, error) {
	withRegistry := len(plan.spec.Registries) > 0
	if withRegistry {
		var err error
		if c.registry, err = c.createRegistry(ctx, plan.spec.Registries); err != nil {
			return nil, err
		}
		if err := startRegistry(ctx, d.cli, registrySpec{id: c.registry, runID: c.runID, binary: plan.binary, creds: plan.creds}); err != nil {
			return nil, err
		}
	}
	if plan.agent == nil {
		return proxyEnv(noProxy, withRegistry), nil
	}
	if err := d.addModel(ctx, c, plan.binary, plan.agent); err != nil {
		return nil, err
	}
	return plan.agent.env(withRegistry), nil
}

// createWorkspace creates the workspace container on the run's network
// alone, never on the default bridge. The network is set at creation: a
// container connected afterwards does not get the embedded DNS.
func (c *Container) createWorkspace(ctx context.Context, image string, env []string) (string, error) {
	netName := networkName(c.runID)
	id, err := createContainer(ctx, c.cli, client.ContainerCreateOptions{
		Name: netName + "-workspace",
		Config: &container.Config{
			Image:      image,
			Cmd:        []string{"sleep", "infinity"},
			WorkingDir: workDir,
			Env:        env,
			Labels:     c.labels(),
		},
		HostConfig:       &container.HostConfig{NetworkMode: container.NetworkMode(netName), Runtime: c.runtime},
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{netName: {}}},
	})
	if err != nil {
		return "", fmt.Errorf("workspace: create container from %s: %w", image, err)
	}
	return id, nil
}

// proxyEnv points both spellings tools read at the gateway, except for the
// hosts in direct, and, for a run with a registry proxy, names that
// proxy's URL and keeps it off the gateway. A URL is an address, not a
// credential.
func proxyEnv(direct string, withRegistry bool) []string {
	env := []string{}
	if withRegistry {
		direct += "," + registryAlias
		env = append(env, "FORJA_REGISTRY_URL="+registryURL)
	}
	return append(env,
		"HTTP_PROXY="+gatewayURL, "HTTPS_PROXY="+gatewayURL,
		"http_proxy="+gatewayURL, "https_proxy="+gatewayURL,
		"NO_PROXY="+direct, "no_proxy="+direct,
	)
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
