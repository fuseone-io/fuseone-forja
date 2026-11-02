package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"

	modelproxy "github.com/fuseone-io/fuseone-forja/internal/proxy/model"
)

// model runs the model proxy of one run until ctx ends: the process the
// control plane starts in the model container. The key and the run token
// are read from files, never from the arguments or the environment. A
// misconfiguration exits 2 before anything is written to stdout, so the
// control plane never reads a readiness line from a proxy that is not
// serving.
func model(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts, listen, err := modelOptions(args, stderr)
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) && !errors.Is(err, errUsage) {
			// The exit code is the only channel left when stderr itself fails.
			_, _ = fmt.Fprintf(stderr, "forjad: model: %v\n", err)
		}
		return 2
	}
	opts.Now, opts.Log = utcNow, stdout
	p, err := modelproxy.New(opts)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "forjad: model: %v\n", err)
		return 2
	}
	ln, err := new(net.ListenConfig).Listen(ctx, "tcp", listen)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "forjad: model: %v\n", err)
		return 1
	}
	if err := p.Serve(ctx, ln); err != nil {
		_, _ = fmt.Fprintf(stderr, "forjad: model: %v\n", err)
		return 1
	}
	return 0
}

// errUsage is a command line the usage text already answered.
var errUsage = errors.New("usage")

// modelFlags is the command line of forjad model.
type modelFlags struct {
	runID, upstream, capRaw, keyFile, tokenFile, listen string
}

func parseModelFlags(args []string, stderr io.Writer) (modelFlags, error) {
	var f modelFlags
	flags := flag.NewFlagSet("model", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&f.runID, "run", "", "id of the run whose agent this proxy serves")
	flags.StringVar(&f.upstream, "upstream", modelproxy.DefaultUpstream, "model API to forward to")
	flags.StringVar(&f.capRaw, "cap", "", "total tokens the run may use")
	flags.StringVar(&f.keyFile, "key-file", "", "file holding the model API key")
	flags.StringVar(&f.tokenFile, "token-file", "", "file holding the run token")
	flags.StringVar(&f.listen, "listen", ":"+modelproxy.Port, "address to serve on")
	if err := flags.Parse(args); err != nil {
		return modelFlags{}, err
	}
	if f.runID == "" || f.keyFile == "" || f.tokenFile == "" || flags.NArg() != 0 {
		usage(stderr)
		return modelFlags{}, errUsage
	}
	return f, nil
}

// modelOptions parses the flags and reads the two secret files. No error
// carries a secret's value.
func modelOptions(args []string, stderr io.Writer) (modelproxy.Options, string, error) {
	f, err := parseModelFlags(args, stderr)
	if err != nil {
		return modelproxy.Options{}, "", err
	}
	up, err := modelproxy.ParseUpstream(f.upstream)
	if err != nil {
		return modelproxy.Options{}, "", err
	}
	tokenCap, err := strconv.ParseInt(f.capRaw, 10, 64)
	if err != nil || tokenCap <= 0 {
		return modelproxy.Options{}, "", errors.New("-cap must be a positive integer")
	}
	key, err := readSecret("-key-file", f.keyFile)
	if err != nil {
		return modelproxy.Options{}, "", err
	}
	token, err := readSecret("-token-file", f.tokenFile)
	if err != nil {
		return modelproxy.Options{}, "", err
	}
	return modelproxy.Options{RunID: f.runID, Upstream: up, Key: key, Token: token, Cap: tokenCap}, f.listen, nil
}

// readSecret is the file's content without surrounding whitespace; a file
// that cannot be read or holds nothing is an error naming the flag.
func readSecret(flagName, path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", flagName, err)
	}
	secret := strings.TrimSpace(string(b))
	if secret == "" {
		return "", fmt.Errorf("%s: empty", flagName)
	}
	return secret, nil
}
