package workspace

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/moby/moby/client"
)

// labelOwner marks every container and network this package creates with
// the instance that created it, so a start sweeps its own dead runs and
// never another instance's live ones on the same daemon.
const labelOwner = "forja.owner"

// ErrNoOwner is a sweep asked of a driver that labels nothing as its own:
// every run's objects would match.
var ErrNoOwner = errors.New("workspace: no owner to sweep for")

// Swept is what a sweep removed: how many containers and networks, and the
// runs they belonged to, sorted and each once. It holds nothing of the
// objects' configuration.
type Swept struct {
	Containers int
	Networks   int
	Runs       []string
}

// labels marks one object of the run with its run and, when the driver
// has one, its owner.
func (c *Container) labels() map[string]string {
	labels := map[string]string{labelRun: c.runID}
	if c.owner != "" {
		labels[labelOwner] = c.owner
	}
	return labels
}

// Sweep removes every container, in any state, and then every network the
// daemon holds labelled as a run of this driver's owner. It is for a start,
// when no run of the owner is alive; one sent while a launch is in flight
// would remove that launch's objects. Every removal is attempted whatever
// the one before it returned, and the result counts what was removed even
// when the error is not nil.
func (d *Docker) Sweep(ctx context.Context) (Swept, error) {
	if d.opts.Owner == "" {
		return Swept{}, ErrNoOwner
	}
	filter := client.Filters{}.Add("label", labelRun, labelOwner+"="+d.opts.Owner)
	runs := map[string]bool{}
	var swept Swept
	var errs []error
	swept.Containers, errs = d.sweepContainers(ctx, filter, runs)
	n, err := d.sweepNetworks(ctx, filter, runs)
	swept.Networks = n
	errs = append(errs, err)
	for id := range runs {
		swept.Runs = append(swept.Runs, id)
	}
	slices.Sort(swept.Runs)
	return swept, errors.Join(errs...)
}

// sweepContainers removes the containers filter matches and adds the run
// of each one removed to runs.
func (d *Docker) sweepContainers(ctx context.Context, filter client.Filters, runs map[string]bool) (int, []error) {
	list, err := d.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filter})
	if err != nil {
		return 0, []error{fmt.Errorf("workspace: sweep: list containers: %w", err)}
	}
	removed, errs := 0, []error{}
	for _, c := range list.Items {
		if err := removeContainer(ctx, d.cli, c.ID); err != nil {
			errs = append(errs, fmt.Errorf("workspace: sweep: %w", err))
			continue
		}
		removed++
		runs[c.Labels[labelRun]] = true
	}
	return removed, errs
}

// sweepNetworks removes the networks filter matches and adds the run of
// each one removed to runs.
func (d *Docker) sweepNetworks(ctx context.Context, filter client.Filters, runs map[string]bool) (int, error) {
	list, err := d.cli.NetworkList(ctx, client.NetworkListOptions{Filters: filter})
	if err != nil {
		return 0, fmt.Errorf("workspace: sweep: list networks: %w", err)
	}
	removed, errs := 0, []error{}
	for _, n := range list.Items {
		if err := removeNetwork(ctx, d.cli, n.ID); err != nil {
			errs = append(errs, fmt.Errorf("workspace: sweep: network of run %s: %w", n.Labels[labelRun], err))
			continue
		}
		removed++
		runs[n.Labels[labelRun]] = true
	}
	return removed, errors.Join(errs...)
}
