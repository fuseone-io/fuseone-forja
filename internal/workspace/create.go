package workspace

import (
	"context"
	"time"

	"github.com/moby/moby/client"
)

// createGrace is how long a create already sent outlives the end of its run.
const createGrace = 30 * time.Second

// createContext is the context a create is sent under. The daemon completes
// a create whose request was cancelled, and the id it answered is then lost
// to Close; the end of the run, cancellation or deadline, therefore reaches
// a create in flight only grace later, so its answer can still come back.
// The run's deadline, when it has one, moves by grace and stays visible. A
// run found ended here sends no create.
func createContext(ctx context.Context, grace time.Duration) (context.Context, context.CancelFunc, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	created, cancel := detachedFrom(ctx, grace)
	stop := context.AfterFunc(ctx, func() {
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-timer.C:
			cancel()
		case <-created.Done():
		}
	})
	return created, func() { stop(); cancel() }, nil
}

// detachedFrom is ctx without its cancellation, with its deadline, if any,
// grace later.
func detachedFrom(ctx context.Context, grace time.Duration) (context.Context, context.CancelFunc) {
	if deadline, ok := ctx.Deadline(); ok {
		return context.WithDeadline(context.WithoutCancel(ctx), deadline.Add(grace))
	}
	return context.WithCancel(context.WithoutCancel(ctx))
}

// createContainer creates one container of the run under createContext and
// returns its id.
func createContainer(ctx context.Context, cli *client.Client, opts client.ContainerCreateOptions) (string, error) {
	ctx, cancel, err := createContext(ctx, createGrace)
	if err != nil {
		return "", err
	}
	defer cancel()
	created, err := cli.ContainerCreate(ctx, opts)
	if err != nil {
		return "", err
	}
	return created.ID, nil
}
