package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/agent"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

// fakeAgent records the task it was given and the deadline it ran under,
// and answers with result, or fails with err. With results set, the n-th
// call answers results[n], the last one once they run out; with failOn set,
// err is returned on that call alone.
type fakeAgent struct {
	calls   *callLog
	result  agent.Result
	results []agent.Result
	err     error
	failOn  int

	mu       sync.Mutex
	tasks    []agent.Task
	deadline time.Time
}

// Run exports, as the real runner does, only into the bundle path the task
// names, which the control plane must have made ready and which, as for
// the workspace's export, must not exist yet.
func (f *fakeAgent) Run(ctx context.Context, t agent.Task) (agent.Result, error) {
	if f.calls != nil {
		f.calls.add("agent")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks = append(f.tasks, t)
	f.deadline, _ = ctx.Deadline()
	res := f.result
	if len(f.results) > 0 {
		res = f.results[min(len(f.tasks), len(f.results))-1]
	}
	if res.Exported {
		if err := writeBundle(t.Bundle); err != nil {
			return agent.Result{ExitCode: domain.NoExitCode}, err
		}
	}
	if f.failOn != 0 && f.failOn != len(f.tasks) {
		return res, nil
	}
	return res, f.err
}

func writeBundle(path string) error {
	b, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = b.WriteString("# v2 git bundle\n")
	return errors.Join(err, b.Close())
}

func (f *fakeAgent) taskList() []agent.Task {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agent.Task(nil), f.tasks...)
}

// exported is the result of an agent that exited 0 and committed.
func exported() agent.Result {
	return agent.Result{ExitCode: 0, Exported: true, Tokens: spent}
}

func (f *fakeAgent) task(t *testing.T) agent.Task {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.tasks) != 1 {
		t.Fatalf("agent ran %d times, want once", len(f.tasks))
	}
	return f.tasks[0]
}

var implementStatement = domain.Issue{Number: implementIssue, Title: "Meter the tokens", Body: "## What to build\n\nA proxy.\n"}

// checkoutContract differs from the API's contract in image and wall
// clock, so a task built from the wrong one is told apart.
const checkoutContract = "version: 1\nimage: golang:1.26\ngates:\n  - name: build\n    run: make build\n" +
	"limits:\n  wall_clock: 20m\nagent_context:\n  - CLAUDE.md\n  - docs/\n"

// agentHead is the commit the agent left on its branch.
const agentHead = "04915d552e6e507929c4c734b4f4a3a31ae05820"

func implementCheckout() *fakeCheckout {
	return &fakeCheckout{
		sha: planSHA, branch: "main", dir: "/tmp/forja-plan-implement",
		head: agentHead, adoptedDir: "/tmp/forja-plan-implement-adopted",
		files: map[string]string{
			".forja.yaml": checkoutContract,
			"CLAUDE.md":   "# Rules\n",
			"docs/b.md":   "b\n",
			"docs/a.md":   "a\n",
		},
		dirs: map[string]bool{"docs": true},
	}
}

var spent = domain.TokenUsage{Input: 1000, CacheCreation: 200, CacheRead: 30, Output: 400}

// The agent works on the base branch's checkout with the contract and the
// agent_context of that commit, the issue as GitHub has it, and the
// contract's wall clock as its deadline.
func TestImplement_agentTask_fromBaseCheckoutUnderWallClock(t *testing.T) {
	t.Parallel()
	fx := newImplementFixture(t, contractWithWallClock, nil)

	decodeImplement(t, callImplement(t, fx.cs, implementIssue))
	if fx.cloner.branch != "main" || fx.cloner.repo != doctorRepo {
		t.Fatalf("cloned %s at %q, want %s at the input's base branch", fx.cloner.repo, fx.cloner.branch, doctorRepo)
	}
	assertTaskFromCheckout(t, fx.agent.task(t))
	if left := time.Until(fx.agent.deadline); left > 20*time.Minute || left < 19*time.Minute {
		t.Fatalf("agent deadline in %v, want the checkout contract's 20m", left)
	}
	if fx.cloner.co.closed != 1 {
		t.Fatalf("checkout closed %d times, want once", fx.cloner.co.closed)
	}
}

func assertTaskFromCheckout(t *testing.T, task agent.Task) {
	t.Helper()
	if task.RunID != "run-1" || task.Tree != "/tmp/forja-plan-implement" || task.Contract.Image != "golang:1.26" || task.Issue != implementStatement {
		t.Fatalf("task = %+v, want run-1 on the checkout's tree with its contract and the issue", task)
	}
	if task.Branch != "forja/12" || task.BaseSHA != planSHA || task.Bundle == "" {
		t.Fatalf("task branch %q base %q bundle %q, want forja/12 started at the base with a bundle path", task.Branch, task.BaseSHA, task.Bundle)
	}
	want := []agent.File{{Path: "CLAUDE.md", Content: "# Rules\n"}, {Path: "docs/a.md", Content: "a\n"}, {Path: "docs/b.md", Content: "b\n"}}
	if !reflect.DeepEqual(task.Context, want) {
		t.Fatalf("context = %+v, want %+v", task.Context, want)
	}
}

// statusOf calls forja.status on the run forja.implement named.
func statusOf(t *testing.T, fx *implementFixture) map[string]json.RawMessage {
	t.Helper()
	return statusWire(t, fx.cs, "run-1")
}

func TestImplement_outcomes_stateReasonAndTokensInStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		result     agent.Result
		err        error
		wantState  string
		wantReason string
	}{
		{name: "agent exits 0 and committed", result: exported(), wantState: "succeeded"},
		{name: "agent exits 0 without committing", result: agent.Result{ExitCode: 0, Tokens: spent}, wantState: "failed", wantReason: `"no_commits"`},
		{name: "agent exits 1", result: agent.Result{ExitCode: 1, Tokens: spent}, wantState: "failed"},
		{name: "deadline", result: agent.Result{ExitCode: domain.NoExitCode, TimedOut: true, Tokens: spent}, wantState: "failed"},
		{name: "cap", result: agent.Result{ExitCode: domain.NoExitCode, Capped: true, Tokens: spent}, wantState: "failed", wantReason: `"token_cap"`},
		{name: "cap, agent exited 0 and exported", result: agent.Result{ExitCode: 0, Capped: true, Exported: true, Tokens: spent}, wantState: "failed", wantReason: `"token_cap"`},
		{name: "infrastructure error", result: agent.Result{ExitCode: domain.NoExitCode, Tokens: spent}, err: errors.New("daemon gone"), wantState: "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fx := newAgentFixture(t, contractWithWallClock, nil, &fakeAgent{result: tt.result, err: tt.err})

			res := callImplement(t, fx.cs, implementIssue)
			if res.IsError != (tt.err != nil) || (tt.err != nil && !strings.Contains(textOf(res), "daemon gone")) {
				t.Fatalf("IsError = %v, text %q; want a tool error only for the infrastructure error", res.IsError, textOf(res))
			}
			wire := statusOf(t, fx)
			if string(wire["state"]) != `"`+tt.wantState+`"` || string(wire["failure_reason"]) != tt.wantReason {
				t.Fatalf("state %s reason %s, want %s %s", wire["state"], wire["failure_reason"], tt.wantState, tt.wantReason)
			}
			if string(wire["tokens_in"]) != "1230" || string(wire["tokens_out"]) != "400" {
				t.Fatalf("tokens_in %s tokens_out %s, want 1230 and 400 on every outcome", wire["tokens_in"], wire["tokens_out"])
			}
			if released := fx.claims.releasedClaims(); len(released) != 1 {
				t.Fatalf("released %d claims, want the run's one", len(released))
			}
		})
	}
}

// The egress the agent's workspace was refused is the run's, as for a
// doctor run.
func TestImplement_agentEgress_inStatus(t *testing.T) {
	t.Parallel()
	fx := newAgentFixture(t, contractWithWallClock, nil, &fakeAgent{result: agent.Result{Egress: deniedEgress()}})

	decodeImplement(t, callImplement(t, fx.cs, implementIssue))
	if wire := statusOf(t, fx); string(wire["egress_denied_count"]) != "5" {
		t.Fatalf("egress_denied_count = %s, want 5", wire["egress_denied_count"])
	}
}

// What fails before the agent runs fails the run and still frees the
// issue; nothing is metered.
func TestImplement_beforeAgentFails_runFailedClaimReleased(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*implementFixture){
		"clone":    func(fx *implementFixture) { fx.cloner.err = errors.New("clone refused") },
		"contract": func(fx *implementFixture) { fx.cloner.co.files[".forja.yaml"] = "version: 1\n" },
		"agent_context": func(fx *implementFixture) {
			fx.cloner.co.failPath, fx.cloner.co.failErr = "CLAUDE.md", errors.New("disk")
		},
		"contract gone": func(fx *implementFixture) { delete(fx.cloner.co.files, ".forja.yaml") },
		"issue":         func(fx *implementFixture) { fx.issues.err = errors.New("issue unreadable") },
	}
	for name, breakIt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fx := newImplementFixture(t, contractWithWallClock, nil)
			breakIt(fx)

			res := callImplement(t, fx.cs, implementIssue)
			if !res.IsError {
				t.Fatalf("IsError = false when %s fails", name)
			}
			if got := fx.calls.list(); !reflect.DeepEqual(got, []string{"claim", "put running", "clone", "release", "put failed"}) {
				t.Fatalf("calls = %q, want the claim released and the run failed without the agent", got)
			}
			if wire := statusOf(t, fx); string(wire["tokens_in"]) != "0" {
				t.Fatalf("tokens_in = %s, want 0", wire["tokens_in"])
			}
		})
	}
}

// A run that never called the model says so with zeros, never with a
// missing key, and has no reason.
func TestStatus_noAgentCall_zeroTokensOnTheWire(t *testing.T) {
	t.Parallel()
	store := runstore.NewMemory()
	mustPut(t, store, terminalRun("run-quiet"))
	cs := connect(t, store)

	wire := statusWire(t, cs, "run-quiet")
	if string(wire["tokens_in"]) != "0" || string(wire["tokens_out"]) != "0" {
		t.Fatalf("tokens_in %s tokens_out %s, want 0 and 0", wire["tokens_in"], wire["tokens_out"])
	}
	if _, present := wire["failure_reason"]; present {
		t.Fatalf("failure_reason present for a run with none: %s", wire["failure_reason"])
	}
}

// Without an agent the tool refuses before reserving anything and says
// what to configure.
func TestImplement_noAgent_notWiredNamingTheVariables(t *testing.T) {
	t.Parallel()
	calls := &callLog{}
	contracts := &fakeContracts{src: domain.ContractSource{SHA: doctorSHA, Body: contractWithWallClock}}
	deps := testDeps(&recordingRuns{Memory: runstore.NewMemory(), calls: calls}, contracts, &fakeClaims{calls: calls})
	deps.Agent = nil
	cs := connectDeps(t, deps)

	res := callImplement(t, cs, implementIssue)
	for _, want := range []string{"not wired", "FORJA_MODEL_API_KEY", "FORJA_MODEL_TOKEN_CAP", "FORJA_AGENT_BINARY"} {
		if !res.IsError || !strings.Contains(textOf(res), want) {
			t.Errorf("IsError %v, text %q; want it to name %s", res.IsError, textOf(res), want)
		}
	}
	if got := calls.list(); len(got) != 0 {
		t.Fatalf("calls = %q, want none", got)
	}
}
