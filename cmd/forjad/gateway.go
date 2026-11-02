package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"

	"github.com/fuseone-io/fuseone-forja/internal/proxy/egress"
)

// gateway runs the egress gateway of one run until ctx ends: the process
// the control plane starts in the gateway container. A misconfiguration
// exits 2 before anything is written to stdout, so the control plane never
// reads a readiness line from a gateway that is not serving.
func gateway(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("gateway", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runID := flags.String("run", "", "id of the run whose workspace this gateway serves")
	allowRaw := flags.String("allow", "", "comma-separated host:port a tunnel may reach")
	listen := flags.String("listen", ":"+egress.Port, "address to serve on")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *runID == "" || flags.NArg() != 0 {
		usage(stderr)
		return 2
	}
	allow, err := egress.ParseAllow(*allowRaw)
	if err != nil {
		// The exit code is the only channel left when stderr itself fails.
		_, _ = fmt.Fprintf(stderr, "forjad: gateway: %v\n", err)
		return 2
	}
	g, err := egress.New(egress.Options{RunID: *runID, Allow: allow, Now: utcNow, Log: stdout})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "forjad: gateway: %v\n", err)
		return 2
	}
	ln, err := new(net.ListenConfig).Listen(ctx, "tcp", *listen)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "forjad: gateway: %v\n", err)
		return 1
	}
	if err := g.Serve(ctx, ln); err != nil {
		_, _ = fmt.Fprintf(stderr, "forjad: gateway: %v\n", err)
		return 1
	}
	return 0
}
