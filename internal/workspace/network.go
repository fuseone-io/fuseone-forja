package workspace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// networkPrefix starts the name of every run network.
const networkPrefix = "forja-run-"

// createNetwork creates the user-defined bridge one launch lives on. A
// container created on it gets the daemon's embedded DNS, which resolves a
// sidecar by its alias, and the daemon isolates it from every other
// bridge, the default one included.
func createNetwork(ctx context.Context, cli *client.Client) (string, error) {
	created, err := cli.NetworkCreate(ctx, networkName(), client.NetworkCreateOptions{
		Driver: "bridge",
		Labels: map[string]string{labelRun: "doctor"},
	})
	if err != nil {
		return "", fmt.Errorf("workspace: create network: %w", err)
	}
	return created.ID, nil
}

// networkName is the prefix and 16 hex digits. rand.Read never returns an
// error: it fills the buffer or stops the process.
func networkName() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return networkPrefix + hex.EncodeToString(b[:])
}

// removeNetwork removes the run network. One the daemon no longer knows,
// or that was never created, is already removed.
func removeNetwork(ctx context.Context, cli *client.Client, id string) error {
	if id == "" {
		return nil
	}
	_, err := cli.NetworkRemove(ctx, id, client.NetworkRemoveOptions{})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("workspace: remove network: %w", err)
	}
	return nil
}
