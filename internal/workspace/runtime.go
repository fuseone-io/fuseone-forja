package workspace

import (
	"context"
	"errors"
	"fmt"

	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
)

// ErrRuntimeUnavailable is the refusal of a launch whose configured
// container runtime the daemon does not list.
var ErrRuntimeUnavailable = errors.New("workspace: container runtime not registered on the daemon")

// checkRuntime asks the daemon whether it lists want, the runtime every
// container of a run is to be created under. Empty is the daemon's default
// and needs no question.
func checkRuntime(ctx context.Context, cli *client.Client, want string) error {
	if want == "" {
		return nil
	}
	info, err := cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return fmt.Errorf("workspace: daemon info: %w", err)
	}
	return runtimeAvailable(info.Info.Runtimes, want)
}

// runtimeAvailable is nil for the daemon's default, empty, and for a name
// among registered, matched exactly as the daemon matches it.
func runtimeAvailable(registered map[string]system.RuntimeWithStatus, want string) error {
	if want == "" {
		return nil
	}
	if _, ok := registered[want]; !ok {
		return fmt.Errorf("%w: %s", ErrRuntimeUnavailable, want)
	}
	return nil
}
