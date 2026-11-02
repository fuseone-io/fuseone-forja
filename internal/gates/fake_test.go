package gates_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/gates"
)

// scripted is what the fake answers for one command.
type scripted struct {
	exit   int
	stdout string
	stderr string
	// truncated says the workspace cut a stream to its tail.
	truncated bool
	// block makes the command wait for ctx to end and fail with ctx's
	// error, the way a gate that outlives the deadline does.
	block bool
	// err is a failure to run at all, as a daemon error would be.
	err error
}

// scriptedService is what the fake answers when a service is started.
type scriptedService struct {
	notReady  bool
	reason    string
	log       string
	truncated bool
	// block makes the start wait for ctx to end, the way a sidecar that is
	// still coming up when the deadline fires does.
	block bool
	// err is a failure to start at all, as a pull or daemon error would be.
	err error
}

// startCall is what one StartService was given.
type startCall struct {
	svc       domain.Service
	readiness time.Duration
}

// fakeWorkspace enforces what a container enforces: nothing runs or
// starts before Launch or after Close, a service name is an alias taken
// once per run, Close tears the started services down with the workspace,
// the gateway's log is gone once Close removed it, and Close is
// idempotent.
type fakeWorkspace struct {
	mu       sync.Mutex
	launched bool
	closed   int
	script   map[string]scripted
	services map[string]scriptedService
	commands []string
	// events is every start and command, in order, then "close".
	events  []string
	starts  map[string]startCall
	running map[string]bool
	// onStart runs on every start, under no lock.
	onStart func()
	// closeErr is what Close returns the first time.
	closeErr error
	// egress is what the gateway refused; egressErr fails reading it.
	egress      domain.EgressReport
	egressErr   error
	egressReads int
}

// Egress fails on a context that already ended, as a daemon call does, and
// after Close, when the gateway and its log no longer exist.
func (w *fakeWorkspace) Egress(ctx context.Context) (domain.EgressReport, error) {
	if err := ctx.Err(); err != nil {
		return domain.EgressReport{}, fmt.Errorf("fake: egress: %w", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.launched || w.closed > 0 {
		return domain.EgressReport{}, errors.New("fake: egress read without a gateway")
	}
	w.egressReads++
	if w.egressErr != nil {
		return domain.EgressReport{}, w.egressErr
	}
	return w.egress, nil
}

func (w *fakeWorkspace) reads() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.egressReads
}

func (w *fakeWorkspace) Exec(ctx context.Context, command string) (gates.ExecResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.usable("exec"); err != nil {
		return gates.ExecResult{}, err
	}
	w.commands = append(w.commands, command)
	w.events = append(w.events, command)
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
	return gates.ExecResult{ExitCode: s.exit, Stdout: []byte(s.stdout), Stderr: []byte(s.stderr), Truncated: s.truncated}, nil
}

func (w *fakeWorkspace) StartService(ctx context.Context, name string, svc domain.Service, readiness time.Duration) (gates.ServiceResult, error) {
	if w.onStart != nil {
		w.onStart()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.usable("start service"); err != nil {
		return gates.ServiceResult{}, err
	}
	if _, taken := w.starts[name]; taken {
		return gates.ServiceResult{}, fmt.Errorf("fake: alias %s already taken on the run network", name)
	}
	if w.starts == nil {
		w.starts, w.running = map[string]startCall{}, map[string]bool{}
	}
	w.starts[name] = startCall{svc: svc, readiness: readiness}
	w.events = append(w.events, "start "+name)
	s, ok := w.services[name]
	if !ok {
		return gates.ServiceResult{}, fmt.Errorf("fake: service %q not scripted", name)
	}
	if s.err != nil {
		return gates.ServiceResult{}, s.err
	}
	w.running[name] = true
	if s.block {
		w.mu.Unlock()
		<-ctx.Done()
		w.mu.Lock()
		return gates.ServiceResult{Log: []byte(s.log)}, ctx.Err()
	}
	return gates.ServiceResult{Ready: !s.notReady, Reason: s.reason, Log: []byte(s.log), Truncated: s.truncated}, nil
}

// usable fails the way a daemon call on a missing container does.
func (w *fakeWorkspace) usable(op string) error {
	if !w.launched {
		return fmt.Errorf("fake: %s before launch", op)
	}
	if w.closed > 0 {
		return fmt.Errorf("fake: %s after close", op)
	}
	return nil
}

// Close fails on a context that already ended, as a daemon call does:
// the runner must remove the workspace with a context of its own.
func (w *fakeWorkspace) Close(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("fake: close: %w", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed++
	w.running = map[string]bool{}
	if w.closed == 1 {
		w.events = append(w.events, "close")
		return w.closeErr
	}
	return nil
}

func (w *fakeWorkspace) ran() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.commands...)
}

func (w *fakeWorkspace) log() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.events...)
}

func (w *fakeWorkspace) started(name string) (startCall, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	call, ok := w.starts[name]
	return call, ok
}

func (w *fakeWorkspace) stillRunning() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.running)
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
	runID string
	image string
	tree  string
	calls int
}

func (l *fakeLauncher) Launch(_ context.Context, runID, image, tree string) (gates.Workspace, error) {
	l.calls++
	l.runID, l.image, l.tree = runID, image, tree
	if l.err != nil {
		return nil, l.err
	}
	l.ws.mu.Lock()
	l.ws.launched = true
	l.ws.mu.Unlock()
	return l.ws, nil
}
