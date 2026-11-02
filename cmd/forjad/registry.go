package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/fuseone-io/fuseone-forja/internal/proxy/registry"
)

// registryProxy runs the registry proxy of one run until ctx ends: the
// process the control plane starts in the registry container. The
// credentials come from a file, never from a flag or the environment,
// which an inspection of the container would show. A misconfiguration
// exits 2 before anything is written to stdout, so the control plane
// never reads a readiness line from a proxy that is not serving.
func registryProxy(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("registry", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runID := flags.String("run", "", "id of the run whose workspace this proxy serves")
	entries := flags.String("registries", "", "comma-separated registry entries the contract declares")
	credentials := flags.String("credentials", "", "path to the JSON file of each entry's read credential")
	listen := flags.String("listen", ":"+registry.Port, "address to serve on")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *runID == "" || *entries == "" || *credentials == "" || flags.NArg() != 0 {
		usage(stderr)
		return 2
	}
	creds, err := registry.LoadCredentials(*credentials)
	if err != nil {
		// The exit code is the only channel left when stderr itself fails.
		_, _ = fmt.Fprintf(stderr, "forjad: registry: %v\n", err)
		return 2
	}
	p, err := registry.New(registry.Options{RunID: *runID, Registries: strings.Split(*entries, ","), Credentials: creds, Now: utcNow, Log: stdout})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "forjad: registry: %v\n", err)
		return 2
	}
	ln, err := new(net.ListenConfig).Listen(ctx, "tcp", *listen)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "forjad: registry: %v\n", err)
		return 1
	}
	if err := p.Serve(ctx, ln); err != nil {
		_, _ = fmt.Fprintf(stderr, "forjad: registry: %v\n", err)
		return 1
	}
	return 0
}
