package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// launch is one agent session: the run and image its workspace is
// launched for, the tree copied in and the prompt the agent reads.
type launch struct {
	runID, image, tree string
	prompt             []byte
}

// deliverFunc brings out of a running workspace what an agent that exited
// 0 on its own left there.
type deliverFunc func(ctx context.Context, ws Workspace, res *Result) error

// session launches the workspace, writes the prompt and runs the agent
// under the cap; an agent that exited 0 on its own is delivered while the
// workspace still runs. Then it halts, reads the proxies and closes the
// workspace, on every path, with the compute from just before the launch
// until Close returned, or until a failed launch returned.
func (r *Runner) session(ctx context.Context, l launch, deliver deliverFunc) (res Result, err error) {
	if r.Launcher == nil {
		return Result{ExitCode: domain.NoExitCode}, errors.New("agent: Runner needs a launcher")
	}
	if r.Now == nil {
		return Result{ExitCode: domain.NoExitCode}, errors.New("agent: Runner needs a clock")
	}
	launched := r.Now()
	ws, err := r.Launcher.LaunchAgent(ctx, l.runID, l.image, l.tree)
	if err != nil {
		return Result{ExitCode: domain.NoExitCode, Compute: r.Now().Sub(launched)}, fmt.Errorf("agent: launch %s: %w", l.image, err)
	}
	detached := context.WithoutCancel(ctx)
	defer func() {
		closeCtx, cancel := context.WithTimeout(detached, closeTimeout)
		defer cancel()
		if closeErr := ws.Close(closeCtx); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("agent: close workspace: %w", closeErr))
		}
		res.Compute = r.Now().Sub(launched)
	}()
	res, err = execute(ctx, ws, l.prompt)
	if err == nil && res.ExitCode == 0 && !res.Capped {
		err = deliver(ctx, ws, &res)
	}
	return res, errors.Join(err, collect(detached, ws, &res))
}
