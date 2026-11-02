package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/agent"
	"github.com/fuseone-io/fuseone-forja/internal/mcp"
	modelproxy "github.com/fuseone-io/fuseone-forja/internal/proxy/model"
	"github.com/fuseone-io/fuseone-forja/internal/workspace"
)

// The environment variables the agent and its model proxy are configured
// from.
const (
	// EnvModelAPIKey is the model API key the proxy holds. Unset,
	// forja.implement has no agent.
	EnvModelAPIKey = "FORJA_MODEL_API_KEY"
	// EnvModelTokenCap is the total tokens, input and output, one run may
	// use. Required with the key.
	EnvModelTokenCap = "FORJA_MODEL_TOKEN_CAP"
	// EnvModelUpstream is the model API the proxy forwards to; unset is the
	// Anthropic API.
	EnvModelUpstream = "FORJA_MODEL_UPSTREAM"
	// EnvAgentBinary is a linux claude for the Docker daemon's
	// architecture. Required with the key.
	EnvAgentBinary = "FORJA_AGENT_BINARY"
)

// agentConfig is the agent's part of the driver's options. on is false
// when no key is set: forja.implement then has no agent. A key with a
// missing or malformed companion is an error that names the variable and
// never carries the key.
func agentConfig(getenv func(string) string) (opts workspace.Options, on bool, err error) {
	key := getenv(EnvModelAPIKey)
	if key == "" {
		return workspace.Options{}, false, nil
	}
	tokenCap, err := strconv.ParseInt(getenv(EnvModelTokenCap), 10, 64)
	if err != nil || tokenCap <= 0 {
		return workspace.Options{}, false, fmt.Errorf("%s: must be a positive integer with %s set", EnvModelTokenCap, EnvModelAPIKey)
	}
	upstream := getenv(EnvModelUpstream)
	if _, err := modelproxy.ParseUpstream(upstream); err != nil {
		return workspace.Options{}, false, fmt.Errorf("%s: %w", EnvModelUpstream, err)
	}
	binary := getenv(EnvAgentBinary)
	if binary == "" {
		return workspace.Options{}, false, fmt.Errorf("%s: must name a linux agent with %s set", EnvAgentBinary, EnvModelAPIKey)
	}
	if _, err := os.Stat(binary); err != nil {
		return workspace.Options{}, false, fmt.Errorf("%s: %w", EnvAgentBinary, err)
	}
	return workspace.Options{
		AgentBinary: binary,
		Model:       workspace.ModelOptions{Key: key, Upstream: upstream, Cap: tokenCap},
	}, true, nil
}

// agentRunner is the agent forja.implement runs, or nil when none is
// configured. It never returns a typed nil inside the interface.
func agentRunner(docker *workspace.Docker, on bool) mcp.AgentRunner {
	if !on {
		return nil
	}
	return &agent.Runner{Launcher: agentLauncher{docker}, Now: time.Now}
}

// agentLauncher adapts workspace.Docker, which returns its own types, to
// the interfaces the agent runner declares.
type agentLauncher struct {
	*workspace.Docker
}

// LaunchAgent says a spent cap in the runner's own terms, which the agent
// package declares without knowing this driver.
func (l agentLauncher) LaunchAgent(ctx context.Context, in agent.Launch) (agent.Workspace, error) {
	// Field for field the same, like the exec result.
	c, err := l.Docker.LaunchAgent(ctx, workspace.AgentLaunch(in))
	if errors.Is(err, workspace.ErrCapSpent) {
		return nil, fmt.Errorf("%w: %w", agent.ErrCapSpent, err)
	}
	if err != nil {
		return nil, err
	}
	return agentContainer{c}, nil
}

type agentContainer struct {
	*workspace.Container
}

func (c agentContainer) Exec(ctx context.Context, command string) (agent.ExecResult, error) {
	res, err := c.Container.Exec(ctx, command)
	// Field for field the same; the conversion makes the compiler refuse an
	// adapter that drops one.
	return agent.ExecResult(res), err
}

func (c agentContainer) Model(ctx context.Context) (agent.ModelReport, error) {
	r, err := c.Container.Model(ctx)
	// Field for field the same, like the exec result.
	return agent.ModelReport(r), err
}

// unavailableAgent is the agent served when no credential is set: the
// missing credential is what the operator has to fix first.
type unavailableAgent struct {
	err error
}

func (u unavailableAgent) Run(context.Context, agent.Task) (agent.Result, error) {
	return agent.Result{}, u.err
}
