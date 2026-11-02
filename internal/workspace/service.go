package workspace

import (
	"context"
	"fmt"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// healthPoll is the pause between two reads of a sidecar's state.
const healthPoll = 100 * time.Millisecond

// ServiceResult is how one sidecar came up, as the control plane read it
// from the daemon. When not Ready, Reason says why in one line and Log is
// the last MaxStreamBytes of the sidecar's own output, stdout and stderr
// interleaved as it wrote them; Truncated says it had more.
type ServiceResult struct {
	Ready     bool
	Reason    string
	Log       []byte
	Truncated bool
}

// StartService pulls svc.Image when the daemon lacks it, starts it on the
// run network under the alias name with svc.Env as its whole environment,
// and waits up to readiness, counted from its start, for the daemon to
// report it healthy. Its environment never reaches the workspace. A
// sidecar that exits or is not healthy in time is a result; the error is
// for a failure to start at all, including ctx ending first. Every
// sidecar created stays tracked for Close, ready or not.
func (c *Container) StartService(ctx context.Context, name string, svc domain.Service, readiness time.Duration) (ServiceResult, error) {
	rule, err := sidecarReadiness(ctx, c.cli, svc.Image)
	if err != nil {
		return ServiceResult{}, fmt.Errorf("workspace: service %s image %s: %w", name, svc.Image, err)
	}
	id, err := c.createSidecar(ctx, name, svc, rule.probe)
	if err != nil {
		return ServiceResult{}, err
	}
	if _, err := c.cli.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		return ServiceResult{}, fmt.Errorf("workspace: start service %s: %w", name, err)
	}
	res, err := c.awaitReady(ctx, id, name, readiness, rule.probed)
	if err != nil || res.Ready {
		return res, err
	}
	res.Log, res.Truncated, err = sidecarLog(ctx, c.cli, id)
	return res, err
}

// sidecarReadiness pulls image when needed and reads, from its config,
// how its readiness is decided.
func sidecarReadiness(ctx context.Context, cli *client.Client, image string) (readiness, error) {
	if err := ensureImage(ctx, cli, image); err != nil {
		return readiness{}, err
	}
	inspect, err := cli.ImageInspect(ctx, image)
	if err != nil {
		return readiness{}, fmt.Errorf("inspect: %w", err)
	}
	cfg := inspect.Config
	if cfg == nil {
		return readinessOf(nil, nil)
	}
	var own []string
	if cfg.Healthcheck != nil {
		own = cfg.Healthcheck.Test
	}
	return readinessOf(cfg.ExposedPorts, own)
}

// createSidecar creates the sidecar on the run network only, with no port
// published, and tracks it before anything else can fail.
func (c *Container) createSidecar(ctx context.Context, name string, svc domain.Service, probe *container.HealthConfig) (string, error) {
	created, err := c.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:       svc.Image,
			Env:         envList(svc.Env),
			Labels:      map[string]string{labelRun: c.runID},
			Healthcheck: probe,
		},
		HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(c.network)},
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
			c.network: {Aliases: []string{name}},
		}},
	})
	if err != nil {
		return "", fmt.Errorf("workspace: create service %s from %s: %w", name, svc.Image, err)
	}
	c.track(created.ID)
	return created.ID, nil
}

// awaitReady reads the sidecar's state until verdict decides, readiness
// passes, or ctx ends.
func (c *Container) awaitReady(ctx context.Context, id, name string, readiness time.Duration, probed bool) (ServiceResult, error) {
	timer := time.NewTimer(readiness)
	defer timer.Stop()
	poll := time.NewTicker(healthPoll)
	defer poll.Stop()
	for {
		inspect, err := c.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if err != nil {
			return ServiceResult{}, fmt.Errorf("workspace: inspect service %s: %w", name, err)
		}
		if res, done := verdict(name, inspect.Container.State, probed); done {
			return res, nil
		}
		select {
		case <-ctx.Done():
			return ServiceResult{}, fmt.Errorf("workspace: service %s: %w", name, ctx.Err())
		case <-timer.C:
			return ServiceResult{Reason: fmt.Sprintf("service %s not healthy within %v", name, readiness)}, nil
		case <-poll.C:
		}
	}
}

// verdict reads one inspection of a sidecar. A probed sidecar is ready
// when the daemon reports its probe healthy; one with nothing to probe is
// ready once running. A sidecar that stopped is not ready, whatever its
// last probe said. done is false while neither holds.
func verdict(name string, state *container.State, probed bool) (res ServiceResult, done bool) {
	switch {
	case state == nil:
		return ServiceResult{}, false
	case !state.Running:
		return ServiceResult{Reason: fmt.Sprintf("service %s exited with code %d", name, state.ExitCode)}, true
	case !probed:
		return ServiceResult{Ready: true}, true
	case state.Health != nil && state.Health.Status == container.Healthy:
		return ServiceResult{Ready: true}, true
	}
	return ServiceResult{}, false
}

// sidecarLog is the tail of what the sidecar wrote so far. It runs with no
// TTY, so the daemon multiplexes the two streams.
func sidecarLog(ctx context.Context, cli *client.Client, id string) ([]byte, bool, error) {
	logs, err := cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return nil, false, fmt.Errorf("workspace: service log: %w", err)
	}
	// The log was read or failed on its own; closing it decides nothing.
	defer func() { _ = logs.Close() }()
	out := newTail(MaxStreamBytes)
	if _, err := stdcopy.StdCopy(out, out, logs); err != nil {
		return out.Bytes(), out.Truncated(), fmt.Errorf("workspace: service log: %w", err)
	}
	return out.Bytes(), out.Truncated(), nil
}
