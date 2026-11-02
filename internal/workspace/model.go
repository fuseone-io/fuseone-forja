package workspace

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/fuseone-io/fuseone-forja/internal/proxy/model"
)

// The model container: the gateway's image and posture, reachable on the
// run's network by one name. Its secrets live in a directory only its user
// can read; the workspace shares no volume with it.
const (
	modelAlias = "forja-model"
	secretsDir = "forja"
	keyFile    = "/" + secretsDir + "/key"
	tokenFile  = "/" + secretsDir + "/token"
	// tokenBytes is the run token's entropy before hex encoding.
	tokenBytes = 32
)

// modelURL is the model API address the agent is given.
const modelURL = "http://" + modelAlias + ":" + model.Port

// ModelOptions configures the model proxy of an agent launch.
type ModelOptions struct {
	// Key is the real API key. It is copied into the model container as a
	// file and is never in an argument, an environment or a log.
	Key string
	// Upstream is the model API, already validated; empty is the default.
	Upstream string
	// Cap is the run's total tokens.
	Cap int64
}

// The refusals of an agent launch the driver cannot make.
var (
	ErrNoAgentBinary = errors.New("workspace: no agent binary: set Options.AgentBinary to a linux agent")
	ErrNoModelKey    = errors.New("workspace: no model key: set Options.Model.Key")
)

// agentSpec is what one agent launch adds to a plain one.
type agentSpec struct {
	binary string
	token  string
	model  ModelOptions
}

// env is the plain workspace's plus the model proxy's address, kept off the
// gateway, and the run token as the agent's API key.
func (a *agentSpec) env(withRegistry bool) []string {
	return append(proxyEnv(noProxy+","+modelAlias, withRegistry),
		"ANTHROPIC_BASE_URL="+modelURL, "ANTHROPIC_API_KEY="+a.token)
}

// LaunchAgent is Launch plus the run's model proxy and the agent: a
// container on the run network that holds the real key and a run token it
// generates for this launch, and the agent installed in the workspace,
// which is given the token and the proxy's address and nothing else. Only
// this launch creates a model proxy.
func (d *Docker) LaunchAgent(ctx context.Context, runID, image, tree string) (*Container, error) {
	if d.opts.Model.Key == "" {
		return nil, ErrNoModelKey
	}
	if d.opts.AgentBinary == "" {
		return nil, ErrNoAgentBinary
	}
	if _, err := os.Stat(d.opts.AgentBinary); err != nil {
		return nil, fmt.Errorf("workspace: agent binary %s: %w", d.opts.AgentBinary, err)
	}
	token, err := newRunToken()
	if err != nil {
		return nil, err
	}
	agent := &agentSpec{binary: d.opts.AgentBinary, token: token, model: d.opts.Model}
	return d.launch(ctx, LaunchSpec{RunID: runID, Image: image, Tree: tree}, agent)
}

// newRunToken is tokenBytes from the system's random source, in hex.
func newRunToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("workspace: run token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// addModel creates the model container, records it in c, hands it the
// binary and its secrets, gives it its way out and waits until it serves.
func (d *Docker) addModel(ctx context.Context, c *Container, binary string, a *agentSpec) error {
	var err error
	if c.model, err = c.createModel(ctx, a.model); err != nil {
		return err
	}
	if err := copyBinary(ctx, d.cli, c.model, binary, gatewayDir, gatewayBin); err != nil {
		return fmt.Errorf("workspace: copy model binary: %w", err)
	}
	owner, _ := strconv.Atoi(gatewayUser)
	err = copyFiles(ctx, d.cli, c.model, "/",
		tarFile{name: secretsDir + "/", mode: 0o500, owner: owner},
		tarFile{name: keyFile[1:], mode: 0o400, owner: owner, size: int64(len(a.model.Key)), body: bytes.NewReader([]byte(a.model.Key))},
		tarFile{name: tokenFile[1:], mode: 0o400, owner: owner, size: int64(len(a.token)), body: bytes.NewReader([]byte(a.token))},
	)
	if err != nil {
		return fmt.Errorf("workspace: copy model secrets: %w", err)
	}
	return startModel(ctx, d.cli, c.model, c.runID)
}

// createModel creates the run's model proxy on its network under the alias
// the workspace is given. Its command names the secret files, never the
// secrets. It is not started.
func (c *Container) createModel(ctx context.Context, opts ModelOptions) (string, error) {
	upstream := opts.Upstream
	if upstream == "" {
		upstream = model.DefaultUpstream
	}
	cmd := []string{
		gatewayDir + "/" + gatewayBin, "model", "-run", c.runID, "-upstream", upstream,
		"-cap", strconv.FormatInt(opts.Cap, 10), "-key-file", keyFile, "-token-file", tokenFile,
	}
	netName := networkName(c.runID)
	created, err := c.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:   netName + "-model",
		Config: &container.Config{Image: gatewayImage, User: gatewayUser, Cmd: cmd, Labels: map[string]string{labelRun: c.runID}},
		HostConfig: &container.HostConfig{
			NetworkMode: container.NetworkMode(netName),
			CapDrop:     []string{"ALL"},
			SecurityOpt: []string{"no-new-privileges"},
			Runtime:     c.runtime,
		},
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
			netName: {Aliases: []string{modelAlias}},
		}},
	})
	if err != nil {
		return "", fmt.Errorf("workspace: create model proxy of run %s: %w", c.runID, err)
	}
	return created.ID, nil
}

// startModel attaches the proxy to the bridge, since its upstream is
// always outside, starts it and waits for its readiness line.
func startModel(ctx context.Context, cli *client.Client, id, runID string) error {
	if _, err := cli.NetworkConnect(ctx, "bridge", client.NetworkConnectOptions{Container: id}); err != nil {
		return fmt.Errorf("workspace: connect model proxy to bridge: %w", err)
	}
	if _, err := cli.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("workspace: start model proxy: %w", err)
	}
	ready := func(r io.Reader) error { return model.AwaitReady(r, runID) }
	if err := awaitReady(ctx, cli, id, ready); err != nil {
		return fmt.Errorf("workspace: model proxy of run %s: %w", runID, err)
	}
	return nil
}

// Model reads the model proxy's log and returns what it metered. A proxy
// that is no longer running may have forwarded what it did not log, so
// its log is refused as evidence; so is a launch that had no proxy.
func (c *Container) Model(ctx context.Context) (model.Report, error) {
	if c.model == "" {
		return model.Report{}, fmt.Errorf("workspace: run %s has no model proxy", c.runID)
	}
	inspect, err := c.cli.ContainerInspect(ctx, c.model, client.ContainerInspectOptions{})
	if err != nil {
		return model.Report{}, fmt.Errorf("workspace: inspect model proxy: %w", err)
	}
	if state := inspect.Container.State; state == nil || !state.Running {
		return model.Report{}, fmt.Errorf("workspace: model proxy of run %s is not running: its log may miss usage", c.runID)
	}
	logs, err := c.cli.ContainerLogs(ctx, c.model, client.ContainerLogsOptions{ShowStdout: true})
	if err != nil {
		return model.Report{}, fmt.Errorf("workspace: read model log: %w", err)
	}
	var report model.Report
	err = readStdout(logs, func(r io.Reader) error {
		var err error
		report, err = model.ReadUsage(r, c.runID)
		return err
	})
	if err != nil {
		return model.Report{}, fmt.Errorf("workspace: model log: %w", err)
	}
	return report, nil
}

// AwaitCap follows the model proxy's log and returns nil once it says the
// run reached its cap. It returns an error when ctx ends first or the
// proxy's output ends without one.
func (c *Container) AwaitCap(ctx context.Context) error {
	if c.model == "" {
		return fmt.Errorf("workspace: run %s has no model proxy", c.runID)
	}
	logs, err := c.cli.ContainerLogs(ctx, c.model, client.ContainerLogsOptions{ShowStdout: true, Follow: true})
	if err != nil {
		return fmt.Errorf("workspace: follow model log: %w", err)
	}
	err = readStdout(logs, func(r io.Reader) error { return model.AwaitCap(r, c.runID) })
	if ctx.Err() != nil {
		return fmt.Errorf("workspace: await cap: %w", ctx.Err())
	}
	return err
}
