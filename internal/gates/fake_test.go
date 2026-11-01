package gates_test

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/fuseone-io/fuseone-forja/internal/gates"
)

// scripted is what the fake answers for one command.
type scripted struct {
	exit   int
	stdout string
	stderr string
	// block makes the command wait for ctx to end and fail with ctx's
	// error, the way a gate that outlives the deadline does.
	block bool
	// err is a failure to run at all, as a daemon error would be.
	err error
}

// fakeWorkspace enforces what a container enforces: nothing runs before
// Launch or after Close, and Close is idempotent.
type fakeWorkspace struct {
	mu       sync.Mutex
	launched bool
	closed   int
	script   map[string]scripted
	commands []string
	// closeErr is what Close returns the first time.
	closeErr error
}

func (w *fakeWorkspace) Exec(ctx context.Context, command string) (gates.ExecResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.launched {
		return gates.ExecResult{}, errors.New("fake: exec before launch")
	}
	if w.closed > 0 {
		return gates.ExecResult{}, errors.New("fake: exec after close")
	}
	w.commands = append(w.commands, command)
	s, ok := w.script[command]
	if !ok {
		return gates.ExecResult{}, fmt.Errorf("fake: command %q not scripted", command)
	}
	if s.err != nil {
		return gates.ExecResult{}, s.err
	}
	if s.block {
		w.mu.Unlock()
		<-ctx.Done()
		w.mu.Lock()
		return gates.ExecResult{ExitCode: -1, Stdout: []byte(s.stdout)}, ctx.Err()
	}
	return gates.ExecResult{ExitCode: s.exit, Stdout: []byte(s.stdout), Stderr: []byte(s.stderr)}, nil
}

func (w *fakeWorkspace) Close(context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed++
	if w.closed == 1 {
		return w.closeErr
	}
	return nil
}

func (w *fakeWorkspace) ran() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.commands...)
}

func (w *fakeWorkspace) closes() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

// fakeLauncher hands out one workspace and records what it was asked for.
type fakeLauncher struct {
	ws    *fakeWorkspace
	err   error
	image string
	tree  string
	calls int
}

func (l *fakeLauncher) Launch(_ context.Context, image, tree string) (gates.Workspace, error) {
	l.calls++
	l.image, l.tree = image, tree
	if l.err != nil {
		return nil, l.err
	}
	l.ws.mu.Lock()
	l.ws.launched = true
	l.ws.mu.Unlock()
	return l.ws, nil
}
