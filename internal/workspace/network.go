package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// maxRunIDLen keeps the names built from a run id within what the daemon
// accepts for a container name.
const maxRunIDLen = 64

// networkName is the run's network, and the prefix of its containers'
// names.
func networkName(runID string) string { return "forja-run-" + runID }

// validRunID accepts what can name a network and a container: an
// alphanumeric first character, then letters, digits, '_', '.' or '-'.
func validRunID(runID string) error {
	if runID == "" || len(runID) > maxRunIDLen {
		return fmt.Errorf("workspace: run id %q must have 1 to %d characters", runID, maxRunIDLen)
	}
	if !alnum(rune(runID[0])) || strings.TrimFunc(runID, nameRune) != "" {
		return fmt.Errorf("workspace: run id %q cannot name a network", runID)
	}
	return nil
}

func alnum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func nameRune(r rune) bool {
	return alnum(r) || r == '_' || r == '.' || r == '-'
}

// createNetwork creates the run's internal network: a bridge with no route
// out, where the only way to the outside is a container also attached
// elsewhere.
func createNetwork(ctx context.Context, cli *client.Client, runID string) (string, error) {
	created, err := cli.NetworkCreate(ctx, networkName(runID), client.NetworkCreateOptions{
		Driver:   "bridge",
		Internal: true,
		Labels:   map[string]string{labelRun: runID},
	})
	if err != nil {
		return "", fmt.Errorf("workspace: create network of run %s: %w", runID, err)
	}
	return created.ID, nil
}

// removeContainer kills and removes id; a container the daemon no longer
// knows is already removed.
func removeContainer(ctx context.Context, cli *client.Client, id string) error {
	if id == "" {
		return nil
	}
	_, err := cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return err
	}
	return nil
}

// removeNetwork removes id; a network the daemon no longer knows is
// already removed.
func removeNetwork(ctx context.Context, cli *client.Client, id string) error {
	if id == "" {
		return nil
	}
	_, err := cli.NetworkRemove(ctx, id, client.NetworkRemoveOptions{})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return err
	}
	return nil
}

// teardown removes the workspace, then the gateway, then the network: a
// network goes only once nothing is attached to it. Every step runs
// whatever the one before did.
func (c *Container) teardown(ctx context.Context) error {
	var errs []error
	if err := removeContainer(ctx, c.cli, c.id); err != nil {
		errs = append(errs, fmt.Errorf("workspace: remove container: %w", err))
	}
	if err := removeContainer(ctx, c.cli, c.gateway); err != nil {
		errs = append(errs, fmt.Errorf("workspace: remove gateway: %w", err))
	}
	if err := removeNetwork(ctx, c.cli, c.network); err != nil {
		errs = append(errs, fmt.Errorf("workspace: remove network: %w", err))
	}
	return errors.Join(errs...)
}
