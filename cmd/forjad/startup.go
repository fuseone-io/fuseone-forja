package main

import (
	"context"
	"io"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/mcp"
)

// defaultStartupTimeout bounds opening the run store and recovering the
// claims a previous process left, so that a database or GitHub that never
// answers fails the start instead of holding the state directory with no
// server behind it.
const defaultStartupTimeout = 2 * time.Minute

func (h host) startupTimeout() time.Duration {
	if h.startup <= 0 {
		return defaultStartupTimeout
	}
	return h.startup
}

// start assembles the tools' dependencies, opens the run store and closes
// and releases what a previous process left, all within the startup
// deadline. The returned func releases the store; the store itself
// outlives the deadline.
func (h host) start(ctx context.Context, stderr io.Writer) (mcp.Deps, func(), error) {
	ctx, cancel := context.WithTimeout(ctx, h.startupTimeout())
	defer cancel()
	deps, store, err := wire(h.getenv, stderr)
	if err != nil {
		return mcp.Deps{}, nil, err
	}
	runs, closeRuns, err := openRuns(ctx, h.getenv)
	if err != nil {
		return mcp.Deps{}, nil, err
	}
	deps.Runs = runs
	if err := recoverClaims(ctx, store, deps, stderr); err != nil {
		closeRuns()
		return mcp.Deps{}, nil, err
	}
	return deps, closeRuns, nil
}
