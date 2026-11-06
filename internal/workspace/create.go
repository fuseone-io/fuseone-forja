package workspace

import (
	"context"
	"sync"
	"time"

	"github.com/moby/moby/client"
)

// createGrace is how long a create already sent outlives the end of its run.
const createGrace = 30 * time.Second

// createContext is the context a create is sent under. The daemon completes
// a create whose request was cancelled, and the id it answered is then lost
// to Close; the end of the run, cancellation or deadline, therefore reaches
// a create in flight only grace later, so its answer can still come back.
// A run that has already ended sends no create at all.
func createContext(ctx context.Context, grace time.Duration) (context.Context, context.CancelFunc, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	created, cancel := context.WithCancel(context.WithoutCancel(ctx))
	var (
		mu       sync.Mutex
		released bool
		timer    *time.Timer
	)
	stop := context.AfterFunc(ctx, func() {
		mu.Lock()
		defer mu.Unlock()
		if !released {
			timer = time.AfterFunc(grace, cancel)
		}
	})
	release := func() {
		stop()
		mu.Lock()
		released = true
		if timer != nil {
			timer.Stop()
		}
		mu.Unlock()
		cancel()
	}
	return created, release, nil
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
