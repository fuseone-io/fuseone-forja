package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/fuseone-io/fuseone-forja/internal/workspace"
)

// sweepFunc removes from the daemon every object of owner's runs and says
// what it removed.
type sweepFunc func(ctx context.Context, owner string) (workspace.Swept, error)

// dockerSweep is the sweep of the daemon the environment points at.
func dockerSweep(ctx context.Context, owner string) (workspace.Swept, error) {
	d, err := workspace.NewDocker(workspace.Options{Owner: owner})
	if err != nil {
		return workspace.Swept{}, err
	}
	return d.Sweep(ctx)
}

func (h host) sweeper() sweepFunc {
	if h.sweep == nil {
		return dockerSweep
	}
	return h.sweep
}

// sweepDeadRuns removes from the daemon what this instance's runs left
// there, and reports on stderr how many containers and networks of which
// runs it removed. At the start no run of this instance is alive, and the
// state directory's lock keeps any other process from being this
// instance. It never fails the start: what it could not remove waits for
// the next one, and the cause goes to stderr. With no state directory
// there is no instance and nothing to sweep, and a start already stopped
// sweeps nothing.
func (h host) sweepDeadRuns(ctx context.Context, stderr io.Writer) {
	owner, err := instanceOwner(h.getenv)
	if (owner == "" && err == nil) || ctx.Err() != nil {
		return
	}
	swept := workspace.Swept{}
	if err == nil {
		swept, err = h.sweeper()(ctx, owner)
	}
	// The exit code is the only channel left when stderr itself fails.
	if swept.Containers+swept.Networks > 0 {
		_, _ = fmt.Fprintf(stderr, "forjad: removed %d containers and %d networks left by dead runs: %s\n",
			swept.Containers, swept.Networks, strings.Join(swept.Runs, ", "))
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "forjad: what dead runs left on the Docker daemon is not all removed: %v\n", err)
	}
}
