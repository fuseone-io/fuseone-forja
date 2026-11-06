package workspace

import "github.com/moby/moby/client"

// NewDockerOn is NewDocker speaking through cli, for the tests that put
// something between the driver and the daemon.
func NewDockerOn(cli *client.Client, opts Options) *Docker {
	return &Docker{cli: cli, opts: opts}
}
