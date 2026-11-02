package agent_test

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/agent"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// fakeWorkspace is an agent workspace in memory. Like the Docker one, it
// refuses every call once closed, and Exec once halted; AwaitCap returns
// nil only once the cap is reached, and an error when its ctx ends.
type fakeWorkspace struct {
	// exec answers the agent's command; nil exits 0 at once.
	exec   func(ctx context.Context) (agent.ExecResult, error)
	report agent.ModelReport
	egress domain.EgressReport
	// modelErr fails the usage read.
	modelErr error
	// exportErr fails the export of the agent's commits.
	exportErr error
	// capReached is closed to say the proxy reached the cap.
	capReached chan struct{}
	// onClose runs inside Close, as the time a removal takes.
	onClose func()

	mu        sync.Mutex
	calls     []string
	commands  []string
	files     map[string][]byte
	halted    bool
	closes    int
	watchDone chan struct{}
	// exported is the base, branch and destination of each export.
	exported [][3]string
}

func newFakeWorkspace() *fakeWorkspace {
	return &fakeWorkspace{capReached: make(chan struct{}), files: map[string][]byte{}, watchDone: make(chan struct{})}
}

func (f *fakeWorkspace) log(call string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	if f.closes > 0 {
		return errors.New("fake: workspace closed")
	}
	return nil
}

func (f *fakeWorkspace) Exec(ctx context.Context, command string) (agent.ExecResult, error) {
	if err := f.log("exec"); err != nil {
		return agent.ExecResult{ExitCode: -1}, err
	}
	f.mu.Lock()
	f.commands = append(f.commands, command)
	halted := f.halted
	f.mu.Unlock()
	if halted {
		return agent.ExecResult{ExitCode: -1}, errors.New("fake: workspace halted")
	}
	if f.exec == nil {
		return agent.ExecResult{}, nil
	}
	return f.exec(ctx)
}

// ExportCommits records what it was asked, and, like Docker, has nothing
// to export from once the workspace is halted or closed.
func (f *fakeWorkspace) ExportCommits(_ context.Context, base, branch, dst string) error {
	if err := f.log("export"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.halted {
		return errors.New("fake: workspace halted")
	}
	f.exported = append(f.exported, [3]string{base, branch, dst})
	return f.exportErr
}

func (f *fakeWorkspace) exports() [][3]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][3]string(nil), f.exported...)
}

func (f *fakeWorkspace) WriteFile(_ context.Context, path string, data []byte) error {
	if err := f.log("write " + path); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[path] = append([]byte(nil), data...)
	return nil
}

func (f *fakeWorkspace) Model(context.Context) (agent.ModelReport, error) {
	if err := f.log("model"); err != nil {
		return agent.ModelReport{}, err
	}
	return f.report, f.modelErr
}

func (f *fakeWorkspace) AwaitCap(ctx context.Context) error {
	defer close(f.watchDone)
	select {
	case <-f.capReached:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *fakeWorkspace) Egress(context.Context) (domain.EgressReport, error) {
	if err := f.log("egress"); err != nil {
		return domain.EgressReport{}, err
	}
	return f.egress, nil
}

func (f *fakeWorkspace) Halt(context.Context) error {
	if err := f.log("halt"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.halted = true
	return nil
}

func (f *fakeWorkspace) Close(context.Context) error {
	if f.onClose != nil {
		f.onClose()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "close")
	f.closes++
	return nil
}

func (f *fakeWorkspace) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeWorkspace) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closes
}

// fakeLauncher hands out ws, or fails.
type fakeLauncher struct {
	ws  *fakeWorkspace
	err error
	// onLaunch runs inside LaunchAgent, as the time a launch takes, whether
	// it fails or not.
	onLaunch func()

	mu                 sync.Mutex
	calls              int
	runID, image, tree string
}

func (l *fakeLauncher) LaunchAgent(_ context.Context, runID, image, tree string) (agent.Workspace, error) {
	if l.onLaunch != nil {
		l.onLaunch()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	l.runID, l.image, l.tree = runID, image, tree
	if l.err != nil {
		return nil, l.err
	}
	return l.ws, nil
}

// clock reads the same instant until a fake moves it, so the only time
// that passes is the time the fakes say their calls took.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock {
	return &clock{t: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)}
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}
