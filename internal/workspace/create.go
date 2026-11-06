package workspace

import (
	"context"
	"time"

	"github.com/moby/moby/client"
)

// createTimeout bounds a create once it is sent, in place of the caller's
// context.
const createTimeout = 30 * time.Second

// createContext is the context a create is sent under. The daemon completes
// a create whose request was cancelled, and the id it answered is then lost
// to Close; a create, once sent, is therefore not cancelled with the run but
// waits for the daemon's answer, for at most createTimeout. A run that has
// already ended sends no create at all.
func createContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	created, cancel := context.WithTimeout(context.WithoutCancel(ctx), createTimeout)
	return created, cancel, nil
}

// createContainer creates one container of the run under createContext and
// returns its id.
func createContainer(ctx context.Context, cli *client.Client, opts client.ContainerCreateOptions) (string, error) {
	ctx, cancel, err := createContext(ctx)
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
