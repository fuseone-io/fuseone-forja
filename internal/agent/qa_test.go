package agent_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/agent"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

func runQA(t *testing.T, ws *fakeWorkspace, tk agent.QATask) (agent.QAResult, error) {
	t.Helper()
	r := &agent.Runner{Launcher: &fakeLauncher{ws: ws}, Now: newClock().now}
	return r.RunQA(context.Background(), tk)
}

// After an exit 0 every candidate is exported from the head to its own
// bundle, in order, while the workspace still runs; the prompt is the QA
// one, the command the agent's, and the workspace closes last.
func TestRunQA_cleanExit_everyCandidateExportedThenClose(t *testing.T) {
	t.Parallel()
	ws := newFakeWorkspace()
	ws.report, ws.egress = metered, denied
	tk := qaTask()

	res, err := runQA(t, ws, tk)
	want := agent.QAResult{ExitCode: 0, Exported: []bool{true, true}, Tokens: metered.Tokens, Egress: denied}
	if err != nil || !reflect.DeepEqual(res, want) {
		t.Fatalf("RunQA = %+v, %v; want %+v", res, err, want)
	}
	wantExports := [][3]string{{qaHead, "forja-qa/1", "/cp/1.bundle"}, {qaHead, "forja-qa/2", "/cp/2.bundle"}}
	if got := ws.exports(); !reflect.DeepEqual(got, wantExports) {
		t.Fatalf("exports = %q, want %q", got, wantExports)
	}
	wantCalls := []string{"write " + agent.PromptPath, "exec", "export", "export", "halt", "model", "egress", "close"}
	if got := ws.callList(); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("calls = %q, want %q", got, wantCalls)
	}
	if string(ws.files[agent.PromptPath]) != string(agent.RenderQA(tk)) || len(ws.commands) != 1 || ws.commands[0] != agent.Command() {
		t.Fatalf("prompt or command is not the QA prompt under the agent's command")
	}
}

// A candidate the agent left at the head or never made is not exported,
// which is a result; the others still are.
func TestRunQA_candidateEmptyOrMissing_notExportedNoError(t *testing.T) {
	t.Parallel()
	for name, refusal := range map[string]error{"no commits": domain.ErrNoCommits, "no branch": domain.ErrNoBranch} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ws := newFakeWorkspace()
			ws.exportErrs = map[string]error{"forja-qa/1": fmt.Errorf("workspace: export forja-qa/1: %w", refusal)}

			res, err := runQA(t, ws, qaTask())
			if err != nil || !reflect.DeepEqual(res.Exported, []bool{false, true}) {
				t.Fatalf("RunQA = %+v, %v; want only forja-qa/2 exported", res, err)
			}
			if ws.closeCount() != 1 {
				t.Fatalf("closed %d times, want once", ws.closeCount())
			}
		})
	}
}

// Any other export failure is an error, and the workspace still closes.
func TestRunQA_exportFails_errorAndClosed(t *testing.T) {
	t.Parallel()
	ws := newFakeWorkspace()
	ws.exportErrs = map[string]error{"forja-qa/2": errors.New("copy refused")}

	res, err := runQA(t, ws, qaTask())
	if err == nil || ws.closeCount() != 1 {
		t.Fatalf("RunQA = %+v, %v, closed %d; want an error and one close", res, err, ws.closeCount())
	}
}

// An agent that exited non-zero, or that the cap stopped, exports
// nothing, and the workspace closes.
func TestRunQA_nonZeroOrCapped_nothingExportedClosed(t *testing.T) {
	t.Parallel()
	tests := map[string]func(ws *fakeWorkspace){
		"exit 1": func(ws *fakeWorkspace) {
			ws.exec = func(context.Context) (agent.ExecResult, error) { return agent.ExecResult{ExitCode: 1}, nil }
		},
		"capped after exit 0": func(ws *fakeWorkspace) { ws.report = agent.ModelReport{Capped: true} },
		"cap mid exec": func(ws *fakeWorkspace) {
			ws.exec = func(ctx context.Context) (agent.ExecResult, error) {
				close(ws.capReached)
				return blockUntilDone(ctx)
			}
		},
	}
	for name, setUp := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ws := newFakeWorkspace()
			setUp(ws)

			res, err := runQA(t, ws, qaTask())
			if err != nil || !reflect.DeepEqual(res.Exported, []bool{false, false}) {
				t.Fatalf("RunQA = %+v, %v; want nothing exported", res, err)
			}
			if name == "capped after exit 0" {
				if !res.Capped {
					t.Fatalf("RunQA = %+v, want capped", res)
				}
				return
			}
			if len(ws.exports()) != 0 || ws.closeCount() != 1 {
				t.Fatalf("exports %q, closed %d; want none and one close", ws.exports(), ws.closeCount())
			}
		})
	}
}

// The deadline ends the run as a timed-out result with nothing exported,
// and the workspace's compute covers the launch to the close.
func TestRunQA_deadline_timedOutComputeClosed(t *testing.T) {
	t.Parallel()
	ws := newFakeWorkspace()
	ws.exec = blockUntilDone
	clock := newClock()
	ws.onClose = func() { clock.advance(3 * time.Second) }
	r := &agent.Runner{Launcher: &fakeLauncher{ws: ws}, Now: clock.now}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	res, err := r.RunQA(ctx, qaTask())
	if err != nil || !res.TimedOut || res.ExitCode != domain.NoExitCode || res.Compute != 3*time.Second {
		t.Fatalf("RunQA = %+v, %v; want timed out with 3s of compute", res, err)
	}
	if len(ws.exports()) != 0 || ws.closeCount() != 1 {
		t.Fatalf("exports %q, closed %d; want none and one close", ws.exports(), ws.closeCount())
	}
}

// A launch that fails is an error that names no candidate and runs
// nothing; the run is still the launch's runID, image and tree.
func TestRunQA_launchFails_errorNothingRun(t *testing.T) {
	t.Parallel()
	ws := newFakeWorkspace()
	l := &fakeLauncher{ws: ws, err: errors.New("daemon gone")}
	r := &agent.Runner{Launcher: l, Now: newClock().now}

	res, err := r.RunQA(context.Background(), qaTask())
	if err == nil || len(ws.callList()) != 0 || !reflect.DeepEqual(res.Exported, []bool{false, false}) {
		t.Fatalf("RunQA = %+v, %v, calls %q; want an error and nothing run", res, err, ws.callList())
	}
	if l.runID != "run-qa" || l.image != "golang:1.26" || l.tree != "/tmp/forja-verify" {
		t.Fatalf("launched %s %s %s, want the task's", l.runID, l.image, l.tree)
	}
}
