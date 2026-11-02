package mcp_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/agent"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
	forja "github.com/fuseone-io/fuseone-forja/internal/mcp"
)

// firstHead is attempt 1's commit; agentHead is the last attempt's.
const firstHead = "7d3f1e2a9b8c4d5e6f7a8b9c0d1e2f3a4b5c6d7e"

// checkoutWithMaxAttempts is the base checkout's contract with the given
// limits.max_attempts.
func checkoutWithMaxAttempts(n int) string {
	return strings.Replace(checkoutContract, "  wall_clock: 20m\n", fmt.Sprintf("  wall_clock: 20m\n  max_attempts: %d\n", n), 1)
}

var firstSpent = domain.TokenUsage{Input: 700, CacheCreation: 100, CacheRead: 20, Output: 300}

// lintStderr is attempt 1's raw stream, which the agent of attempt 2 is
// handed as it was kept.
const lintStderr = "main.go:3:2: x declared and not used (unused)\n"

// retryFixture is the implement fixture where attempt 1's lint fails on
// firstHead and attempt 2 is green on agentHead.
func retryFixture(t *testing.T) *implementFixture {
	t.Helper()
	fx := newAgentFixture(t, contractWithWallClock, nil, &fakeAgent{results: []agent.Result{
		{ExitCode: 0, Exported: true, Tokens: firstSpent}, exported(),
	}})
	fx.gates.reports = []domain.GateReport{failedAt("lint", lintStderr), greenReport()}
	fx.cloner.co.heads = []string{firstHead, agentHead}
	return fx
}

// AC1: the first diff fails lint, the agent fixes it on a second attempt
// and the run delivers one pull request whose evidence shows both.
func TestImplement_lintRedThenGreen_pullRequestWithTwoAttempts(t *testing.T) {
	t.Parallel()
	fx := retryFixture(t)

	out := decodeImplement(t, callImplement(t, fx.cs, implementIssue))
	want := []string{"claim", "put running", "clone", "agent", "gates", "agent", "gates", "push", "pull_request", "status", "release", "put succeeded"}
	if got := fx.calls.list(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	assertSecondTask(t, fx.agent.taskList())
	assertOnePullRequest(t, fx, "| Attempts | 2 of 3 |", "| Head | `"+agentHead+"` |", "| Head | `"+firstHead+"` |", lintStderr)
	both := firstSpent.Add(spent)
	got := outcome{out.State, out.Attempt, out.MaxAttempts, out.HeadSHA, out.TokensIn, out.TokensOut}
	if want := (outcome{"succeeded", 2, 3, agentHead, both.In(), both.Output}); got != want {
		t.Fatalf("output = %+v, want %+v: both attempts' tokens on the last head", got, want)
	}
	assertAttemptViews(t, out.Attempts,
		attemptView{1, firstHead, "lint", firstSpent.In(), firstSpent.Output, 1, 2, lintStderr},
		attemptView{2, agentHead, "", spent.In(), spent.Output, 1, 2, "warn\n"})
	assertStageAttempts(t, stagesOf(t, statusOf(t, fx)),
		"claim:0", "clone:0", "agent:1", "gates:1", "agent:2", "gates:2", "push:0", "pull_request:0", "status:0", "release:0")
}

// outcome is what a run's output says of how it ended.
type outcome struct {
	state                string
	attempt, maxAttempts int
	head                 string
	tokensIn, tokensOut  int64
}

// assertOnePullRequest checks the run pushed its last head once and opened
// one pull request whose body holds every line.
func assertOnePullRequest(t *testing.T, fx *implementFixture, lines ...string) {
	t.Helper()
	if pushes := fx.pusher.got(); len(pushes) != 1 || pushes[0].Head != agentHead {
		t.Fatalf("pushes = %+v, want one of the last head %s", pushes, agentHead)
	}
	prs := fx.prs.got()
	if len(prs) != 1 {
		t.Fatalf("pull requests = %d, want 1", len(prs))
	}
	for _, line := range lines {
		if !strings.Contains(prs[0].Body, line) {
			t.Errorf("pull request body lacks %q", line)
		}
	}
}

// retryView is what the agent of an attempt was handed.
type retryView struct {
	attempt, maxAttempts int
	name, command        string
	exitCode             int
	stderr               string
	spent                domain.TokenUsage
	tree, branch, base   string
}

// assertSecondTask checks attempt 2 was told what failed on attempt 1, with
// the run's tokens spent so far, on the tree holding attempt 1's head, into
// a bundle of its own.
func assertSecondTask(t *testing.T, tasks []agent.Task) {
	t.Helper()
	if len(tasks) != 2 || tasks[0].Retry != nil || tasks[0].Spent != (domain.TokenUsage{}) || tasks[1].Retry == nil {
		t.Fatalf("tasks = %+v, want two, the first with no retry and nothing spent, the second with one", tasks)
	}
	second, r := tasks[1], tasks[1].Retry
	got := retryView{r.Attempt, r.MaxAttempts, r.Failed.Name, r.Failed.Command, r.Failed.ExitCode, string(r.Failed.Stderr), second.Spent, second.Tree, second.Branch, second.BaseSHA}
	want := retryView{2, 3, "lint", "make lint", 1, lintStderr, firstSpent, "/tmp/forja-plan-implement-adopted", runBranch, planSHA}
	if got != want {
		t.Fatalf("second task = %+v\nwant %+v", got, want)
	}
	if tasks[0].Bundle == second.Bundle || filepath.Dir(tasks[0].Bundle) != filepath.Dir(second.Bundle) {
		t.Fatalf("bundles %s and %s, want one per attempt in the run's directory", tasks[0].Bundle, second.Bundle)
	}
}

// attemptView is what an attempt report says, with its steps counted and
// its last gate's stderr.
type attemptView struct {
	number              int
	head, failed        string
	tokensIn, tokensOut int64
	setup, gates        int
	lastStderr          string
}

func assertAttemptViews(t *testing.T, attempts []forja.AttemptReport, want ...attemptView) {
	t.Helper()
	got := make([]attemptView, 0, len(attempts))
	for _, a := range attempts {
		v := attemptView{a.Number, a.HeadSHA, a.FailedStep, a.TokensIn, a.TokensOut, len(a.Setup), len(a.Gates), ""}
		if len(a.Gates) > 0 {
			v.lastStderr = a.Gates[len(a.Gates)-1].Stderr
		}
		got = append(got, v)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attempts = %+v\nwant %+v", got, want)
	}
}

// assertStageAttempts checks the stages in order as name:attempt.
func assertStageAttempts(t *testing.T, stages []forja.StageReport, want ...string) {
	t.Helper()
	got := make([]string, 0, len(stages))
	for _, s := range stages {
		got = append(got, fmt.Sprintf("%s:%d", s.Name, s.Attempt))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stages = %q, want %q", got, want)
	}
}

// AC2: a red gate on every attempt fails the run once the limit is spent,
// with no push, pull request or status, and every attempt's logs.
func TestImplement_redOnEveryAttempt_failedAfterMaxNoPush(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		contract string
		n        int
	}{
		{name: "max_attempts 2", contract: checkoutWithMaxAttempts(2), n: 2},
		{name: "default", contract: checkoutContract, n: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fx := redEveryAttempt(t, tt.contract, tt.n)

			out := decodeImplement(t, callImplement(t, fx.cs, implementIssue))
			assertExhausted(t, fx, tt.n, out)
			assertEveryAttempt(t, tt.n, out.Attempts, out.Evidence)
			assertSpentGrows(t, fx.agent.taskList())
			var wired []forja.AttemptReport
			if err := json.Unmarshal(statusOf(t, fx)["attempts"], &wired); err != nil || !reflect.DeepEqual(wired, out.Attempts) {
				t.Fatalf("status attempts = %+v (%v), want the same as forja.implement's", wired, err)
			}
		})
	}
}

// assertExhausted checks n agent and gates runs, nothing delivered, the
// run failed at the last attempt's step, and the issue freed.
func assertExhausted(t *testing.T, fx *implementFixture, n int, out forja.ImplementOutput) {
	t.Helper()
	if runs, gates := len(fx.agent.taskList()), fx.gates.calls; runs != n || gates != n {
		t.Fatalf("agent ran %d times, gates %d; want %d each", runs, gates, n)
	}
	if calls := len(fx.pusher.got()) + len(fx.prs.got()) + len(fx.statuses.got()); calls != 0 {
		t.Fatalf("%d delivery calls, want none", calls)
	}
	if out.State != "failed" || out.FailureReason != "gate_failed" || out.FailedStep != "test" || out.PullRequest != nil {
		t.Fatalf("state %q reason %q failed_step %q, want failed gate_failed at the last attempt's test", out.State, out.FailureReason, out.FailedStep)
	}
	if released := fx.claims.releasedClaims(); len(released) != 1 {
		t.Fatalf("released %d claims, want 1", len(released))
	}
}

// assertSpentGrows checks each attempt launched with every earlier
// attempt's tokens, the fixture's agent using spent on each.
func assertSpentGrows(t *testing.T, tasks []agent.Task) {
	t.Helper()
	var before domain.TokenUsage
	for i, task := range tasks {
		if task.Spent != before {
			t.Fatalf("attempt %d launched with %+v spent, want every earlier attempt's %+v", i+1, task.Spent, before)
		}
		before = before.Add(spent)
	}
}

// redEveryAttempt is the fixture whose gates fail on each of n attempts,
// lint until the last one, where test fails, each with its own stream.
func redEveryAttempt(t *testing.T, contract string, n int) *implementFixture {
	t.Helper()
	fx := newImplementFixture(t, contractWithWallClock, nil)
	fx.cloner.co.files[".forja.yaml"] = contract
	for i := 1; i <= n; i++ {
		step := "lint"
		if i == n {
			step = "test"
		}
		fx.gates.reports = append(fx.gates.reports, failedAt(step, fmt.Sprintf("attempt %d failed\n", i)))
		fx.cloner.co.heads = append(fx.cloner.co.heads, fmt.Sprintf("%040d", i))
	}
	return fx
}

func assertEveryAttempt(t *testing.T, n int, attempts []forja.AttemptReport, body string) {
	t.Helper()
	if len(attempts) != n {
		t.Fatalf("attempts = %d, want %d", len(attempts), n)
	}
	for i, a := range attempts {
		stream := fmt.Sprintf("attempt %d failed\n", i+1)
		if a.Number != i+1 || a.HeadSHA != fmt.Sprintf("%040d", i+1) || a.Gates[1].Stderr != stream {
			t.Fatalf("attempt %d = %+v, want its own head and stream", i+1, a)
		}
		if !strings.Contains(body, stream) {
			t.Errorf("evidence lacks attempt %d's log", i+1)
		}
	}
	if !strings.Contains(body, fmt.Sprintf("| Attempts | %d of %d |", n, n)) {
		t.Fatalf("evidence does not count %d of %d attempts:\n%s", n, n, body)
	}
}

// AC3: while attempt 2's gates run, forja.status shows it in flight out of
// the limit, with attempt 1 already reported.
func TestStatus_secondAttemptInFlight_showsAttemptAndMax(t *testing.T) {
	t.Parallel()
	fx := retryFixture(t)
	var mid map[string]json.RawMessage
	calls := 0
	fx.gates.during = func() {
		if calls++; calls == 2 {
			mid = statusOf(t, fx)
		}
	}

	decodeImplement(t, callImplement(t, fx.cs, implementIssue))
	if string(mid["attempt"]) != "2" || string(mid["max_attempts"]) != "3" {
		t.Fatalf("attempt %s max_attempts %s during attempt 2, want 2 and 3", mid["attempt"], mid["max_attempts"])
	}
	var attempts []forja.AttemptReport
	if err := json.Unmarshal(mid["attempts"], &attempts); err != nil || len(attempts) != 1 || attempts[0].FailedStep != "lint" {
		t.Fatalf("attempts during attempt 2 = %s (%v), want attempt 1 alone", mid["attempts"], err)
	}
	stages := stagesOf(t, mid)
	if last := stages[len(stages)-1]; last.Name != "gates" || last.Attempt != 2 || last.Outcome != "running" {
		t.Fatalf("stage in flight = %+v, want attempt 2's gates running", last)
	}
}

// An agent that adds nothing on a later attempt hands back the head the
// gates already failed on: the run ends without running them again.
func TestImplement_retryNoNewCommit_noCommitsWithoutRerunningGates(t *testing.T) {
	t.Parallel()
	fx := retryFixture(t)
	fx.cloner.co.heads = []string{firstHead, firstHead}

	out := decodeImplement(t, callImplement(t, fx.cs, implementIssue))
	if len(fx.agent.taskList()) != 2 || fx.gates.calls != 1 || len(fx.pusher.got()) != 0 {
		t.Fatalf("agent %d gates %d pushes %d, want 2, 1 and none", len(fx.agent.taskList()), fx.gates.calls, len(fx.pusher.got()))
	}
	if out.State != "failed" || out.FailureReason != "no_commits" || out.HeadSHA != firstHead || len(out.Attempts) != 1 {
		t.Fatalf("state %q reason %q head %s attempts %d, want failed no_commits on attempt 1's head with one attempt", out.State, out.FailureReason, out.HeadSHA, len(out.Attempts))
	}
	if wire := statusOf(t, fx); out.Attempt != 2 || string(wire["attempt"]) != "2" {
		t.Fatalf("forja.implement attempt %d, forja.status %s; want both on attempt 2, whose agent ended the run", out.Attempt, wire["attempt"])
	}
}

// A red setup step goes back to the agent like a red gate: attempt 2 is
// handed that step, its exit code and its raw streams as they were kept.
func TestImplement_setupRed_nextAttemptHandedTheSetupStep(t *testing.T) {
	t.Parallel()
	fx := retryFixture(t)
	red := domain.GateResult{Name: "setup[0]", Command: "go mod download", Status: domain.StepFailed, ExitCode: 2, Stdout: []byte("fetching\n"), Stderr: []byte("module not found\n")}
	fx.gates.reports[0] = domain.GateReport{Setup: []domain.GateResult{red}, FailedStep: "setup[0]"}

	decodeImplement(t, callImplement(t, fx.cs, implementIssue))
	tasks := fx.agent.taskList()
	if len(tasks) != 2 || tasks[1].Retry == nil || !reflect.DeepEqual(tasks[1].Retry.Failed, red) {
		t.Fatalf("tasks = %+v, want attempt 2 handed the red setup step %+v", tasks, red)
	}
}

// Only a red gate goes back to the agent: an attempt whose agent was
// capped, stopped, failed or broke ends the run, with what it used.
func TestImplement_retryAgentCapped_tokenCapNoFurtherAttempt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		second  agent.Result
		err     error
		reason  string
		isError bool
	}{
		{name: "capped", second: agent.Result{ExitCode: domain.NoExitCode, Capped: true, Tokens: spent}, reason: "token_cap"},
		{name: "exit 1", second: agent.Result{ExitCode: 1, Tokens: spent}},
		{name: "deadline", second: agent.Result{ExitCode: domain.NoExitCode, TimedOut: true, Tokens: spent}},
		{name: "infrastructure", second: agent.Result{ExitCode: domain.NoExitCode, Tokens: spent}, err: errors.New("daemon gone"), isError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fx := retryFixture(t)
			fx.agent.results[1] = tt.second
			fx.agent.err, fx.agent.failOn = tt.err, 2
			fx.cloner.co.files[".forja.yaml"] = checkoutWithMaxAttempts(5)

			res := callImplement(t, fx.cs, implementIssue)
			if res.IsError != tt.isError {
				t.Fatalf("IsError = %v, want %v: %s", res.IsError, tt.isError, textOf(res))
			}
			if len(fx.agent.taskList()) != 2 || fx.gates.calls != 1 || len(fx.pusher.got()) != 0 {
				t.Fatalf("agent %d gates %d pushes %d, want 2, 1 and none", len(fx.agent.taskList()), fx.gates.calls, len(fx.pusher.got()))
			}
			wire := statusOf(t, fx)
			if reason := strings.Trim(string(wire["failure_reason"]), `"`); reason != tt.reason || string(wire["state"]) != `"failed"` {
				t.Fatalf("state %s reason %q, want failed %q", wire["state"], reason, tt.reason)
			}
			if both := firstSpent.Add(spent); string(wire["tokens_in"]) != fmt.Sprint(both.In()) {
				t.Fatalf("tokens_in = %s, want both attempts' %d", wire["tokens_in"], both.In())
			}
		})
	}
}

// The wall clock is one for every attempt: a red gate that came back after
// it passed starts no new attempt.
func TestImplement_deadlinePassedAfterRedGate_noNewAttempt(t *testing.T) {
	t.Parallel()
	fx := retryFixture(t)
	fx.cloner.co.files[".forja.yaml"] = strings.Replace(checkoutContract, "wall_clock: 20m", "wall_clock: 100ms", 1)
	fx.gates.waitDeadline = true

	out := decodeImplement(t, callImplement(t, fx.cs, implementIssue))
	if len(fx.agent.taskList()) != 1 || fx.gates.calls != 1 || len(fx.pusher.got()) != 0 {
		t.Fatalf("agent %d gates %d pushes %d, want 1, 1 and none", len(fx.agent.taskList()), fx.gates.calls, len(fx.pusher.got()))
	}
	if out.State != "failed" || out.FailureReason != "gate_failed" || out.FailedStep != "lint" {
		t.Fatalf("state %q reason %q failed_step %q, want failed gate_failed at lint", out.State, out.FailureReason, out.FailedStep)
	}
}

// The attempt limit is the base commit's: the agent raising it in its own
// tree changes nothing, nor does the contract the lease was sized from.
func TestImplement_contractChangedByAgent_attemptLimitFromBase(t *testing.T) {
	t.Parallel()
	fx := redEveryAttempt(t, checkoutWithMaxAttempts(2), 2)
	fx.cloner.co.adoptedDir = t.TempDir()
	raised := strings.Replace(checkoutWithMaxAttempts(5), "image: golang:1.26", "image: golang:9.99", 1)
	if err := os.WriteFile(filepath.Join(fx.cloner.co.adoptedDir, ".forja.yaml"), []byte(raised), 0o644); err != nil {
		t.Fatal(err)
	}

	out := decodeImplement(t, callImplement(t, fx.cs, implementIssue))
	if runs := len(fx.agent.taskList()); runs != 2 || out.MaxAttempts != 2 || fx.gates.calls != 2 {
		t.Fatalf("agent ran %d times, gates %d, max_attempts %d; want 2, 2 and the base's 2", runs, fx.gates.calls, out.MaxAttempts)
	}
	for _, task := range fx.agent.taskList() {
		if task.Contract.Limits.MaxAttempts != 2 || task.Contract.Image != "golang:1.26" {
			t.Fatalf("task contract %+v, want the base checkout's", task.Contract)
		}
	}
}

// Every agent workspace of the run adds to its egress report: attempt 2's
// denials come after attempt 1's, none replaced.
func TestImplement_agentEgress_mergedAcrossAttempts(t *testing.T) {
	t.Parallel()
	fx := retryFixture(t)
	for i := range fx.agent.results {
		fx.agent.results[i].Egress = deniedEgress()
	}

	decodeImplement(t, callImplement(t, fx.cs, implementIssue))
	if wire := statusOf(t, fx); string(wire["egress_denied_count"]) != "10" {
		t.Fatalf("egress_denied_count = %s, want both agents' 10", wire["egress_denied_count"])
	}
}

// Every gates workspace of the run adds to its registry report: attempt 2's
// accesses come after attempt 1's, none replaced.
func TestImplement_registryAccess_mergedAcrossGatesRuns(t *testing.T) {
	t.Parallel()
	fx := retryFixture(t)
	for i := range fx.gates.reports {
		fx.gates.reports[i].Registry = registryAccess()
	}

	decodeImplement(t, callImplement(t, fx.cs, implementIssue))
	wire := statusOf(t, fx)
	var events []forja.RegistryEvent
	if err := json.Unmarshal(wire["registry_access"], &events); err != nil {
		t.Fatal(err)
	}
	if string(wire["registry_access_count"]) != "8" || len(events) != 4 {
		t.Fatalf("registry_access_count %s with %d listed, want both runs' 8 with 4 listed", wire["registry_access_count"], len(events))
	}
}
