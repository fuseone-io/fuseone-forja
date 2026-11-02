package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/agent"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/github"
	forja "github.com/fuseone-io/fuseone-forja/internal/mcp"
)

// shortWallClock is a checkout contract whose wall clock ends while the
// run works.
var shortWallClock = checkoutWithWallClock("50ms")

// untilDeadlineAgent works until its context ends and answers as the real
// runner does: the deadline is a timed-out result, a cancellation an
// error.
type untilDeadlineAgent struct {
	calls *callLog
}

func (a untilDeadlineAgent) Run(ctx context.Context, _ agent.Task) (agent.Result, error) {
	a.calls.add("agent")
	<-ctx.Done()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return agent.Result{ExitCode: domain.NoExitCode, TimedOut: true, Tokens: spent}, nil
	}
	return agent.Result{ExitCode: domain.NoExitCode, Tokens: spent}, ctx.Err()
}

// untilDeadlineGates runs its first gate until the deadline and reports it
// timed out, as the real runner does.
type untilDeadlineGates struct {
	calls *callLog
}

func (g untilDeadlineGates) Run(ctx context.Context, _ string, _ domain.Contract, _ string) (domain.GateReport, error) {
	g.calls.add("gates")
	<-ctx.Done()
	return domain.GateReport{
		Gates:      []domain.GateResult{{Name: "build", Command: "make build", Status: domain.StepTimedOut, ExitCode: domain.NoExitCode}},
		FailedStep: "build",
	}, nil
}

// untilDeadlinePusher pushes until its context ends and fails with it.
type untilDeadlinePusher struct {
	calls *callLog
}

func (p untilDeadlinePusher) Push(ctx context.Context, _ github.PushRequest) error {
	p.calls.add("push")
	<-ctx.Done()
	return ctx.Err()
}

// cappedUntilDeadlineAgent is capped and returns only once the deadline
// fired too.
type cappedUntilDeadlineAgent struct{}

func (cappedUntilDeadlineAgent) Run(ctx context.Context, _ agent.Task) (agent.Result, error) {
	<-ctx.Done()
	return agent.Result{ExitCode: domain.NoExitCode, Capped: true, Tokens: spent}, nil
}

// limitsFixture is the implement fixture with the checkout's contract
// replaced and any dependency rewired; calls is the fixture's log.
func limitsFixture(t *testing.T, checkout string, rewire func(d *forja.Deps, calls *callLog)) *implementFixture {
	t.Helper()
	fx := newWiredFixture(t, contractWithWallClock, fixtureWiring{deps: func(d *forja.Deps) {
		if rewire != nil {
			rewire(d, d.Runs.(*recordingRuns).calls)
		}
	}})
	fx.cloner.co.files[".forja.yaml"] = checkout
	return fx
}

// assertWallClockResult checks the run ended as a result, failed for its
// wall clock in the output and in forja.status, with the issue freed once,
// the checkout discarded and no pull request or status.
func assertWallClockResult(t *testing.T, fx *implementFixture, res *sdk.CallToolResult) {
	t.Helper()
	out := decodeImplement(t, res)
	if out.State != "failed" || out.FailureReason != "wall_clock" {
		t.Fatalf("output state %q reason %q, want failed wall_clock", out.State, out.FailureReason)
	}
	wire := statusOf(t, fx)
	if string(wire["state"]) != `"failed"` || string(wire["failure_reason"]) != `"wall_clock"` {
		t.Fatalf("status state %s reason %s, want failed wall_clock", wire["state"], wire["failure_reason"])
	}
	if released := fx.claims.releasedClaims(); len(released) != 1 {
		t.Fatalf("released %d claims, want the run's one", len(released))
	}
	if fx.cloner.co.closed != 1 {
		t.Fatalf("checkout closed %d times, want once", fx.cloner.co.closed)
	}
	if n := len(fx.prs.got()) + len(fx.statuses.got()); n != 0 {
		t.Fatalf("a run its wall clock ended made %d pull request or status calls, want none", n)
	}
}

func TestImplement_wallClockFiresDuringAgent_failedWallClockClaimReleasedCheckoutClosed(t *testing.T) {
	t.Parallel()
	fx := limitsFixture(t, shortWallClock, func(d *forja.Deps, calls *callLog) { d.Agent = untilDeadlineAgent{calls: calls} })

	assertWallClockResult(t, fx, callImplement(t, fx.cs, implementIssue))
	if got, want := fx.calls.list(), []string{"claim", "put running", "clone", "agent", "release", "put failed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q, want %q: no push, pull request or status", got, want)
	}
	if wire := statusOf(t, fx); string(wire["tokens_in"]) != "1230" || string(wire["wall_clock_seconds"]) != "0.05" {
		t.Fatalf("tokens_in %s wall_clock_seconds %s, want what the agent metered under 0.05s", wire["tokens_in"], wire["wall_clock_seconds"])
	}
}

// A gate the deadline killed is the wall clock's doing, not a red gate.
func TestImplement_wallClockFiresDuringGates_reasonWallClockNotGateFailed(t *testing.T) {
	t.Parallel()
	fx := limitsFixture(t, shortWallClock, func(d *forja.Deps, calls *callLog) { d.Gates = untilDeadlineGates{calls: calls} })

	assertWallClockResult(t, fx, callImplement(t, fx.cs, implementIssue))
	if len(fx.pusher.got()) != 0 {
		t.Fatalf("pushed %d times after the wall clock, want never", len(fx.pusher.got()))
	}
	if wire := statusOf(t, fx); !strings.Contains(string(wire["gates"]), `"timed_out"`) {
		t.Fatalf("status gates = %s, want the gate the deadline killed", wire["gates"])
	}
}

// A delivery call the deadline cut is the run's result too, never a tool
// error.
func TestImplement_wallClockFiresDuringPush_resultNotToolError(t *testing.T) {
	t.Parallel()
	fx := limitsFixture(t, shortWallClock, func(d *forja.Deps, calls *callLog) { d.Pusher = untilDeadlinePusher{calls: calls} })

	assertWallClockResult(t, fx, callImplement(t, fx.cs, implementIssue))
	assertFailedAt(t, fx, "push")
}

// The token cap stays the reason when the deadline fired as well: the cap
// is what the agent ran into.
func TestImplement_capAndWallClock_reasonTokenCap(t *testing.T) {
	t.Parallel()
	fx := limitsFixture(t, shortWallClock, func(d *forja.Deps, _ *callLog) { d.Agent = cappedUntilDeadlineAgent{} })

	if out := decodeImplement(t, callImplement(t, fx.cs, implementIssue)); out.State != "failed" || out.FailureReason != "token_cap" {
		t.Fatalf("state %q reason %q, want failed token_cap", out.State, out.FailureReason)
	}
}

// Both readers show the limits the run worked under: the checkout's, with
// the defaults filled for what it omits.
func TestImplement_limitsOnTheWire_effectiveInOutputAndStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		api      []byte
		checkout string
		wantWall string
		wantMax  string
	}{
		{name: "checkout sets the wall clock", api: contractWithWallClock, checkout: checkoutWithWallClock("20m"), wantWall: "1200", wantMax: "400"},
		{name: "checkout sets no limits", api: apiContractNoLimits, checkout: checkoutWithWallClock(""), wantWall: "1800", wantMax: "400"},
		{name: "checkout sets both", api: contractWithWallClock, checkout: checkoutWithWallClock("20m") + "  max_diff_lines: 900\n", wantWall: "1200", wantMax: "900"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fx := newImplementFixture(t, tt.api, nil)
			fx.cloner.co.files[".forja.yaml"] = tt.checkout

			output := wireOf(t, callImplement(t, fx.cs, implementIssue))
			for reader, wire := range map[string]map[string]json.RawMessage{"output": output, "status": statusOf(t, fx)} {
				if string(wire["wall_clock_seconds"]) != tt.wantWall || string(wire["max_diff_lines"]) != tt.wantMax {
					t.Errorf("%s wall_clock_seconds %s max_diff_lines %s, want %s and %s", reader, wire["wall_clock_seconds"], wire["max_diff_lines"], tt.wantWall, tt.wantMax)
				}
			}
		})
	}
}

// checkoutWithMaxDiff is the checkout's contract with the given
// limits.max_diff_lines.
func checkoutWithMaxDiff(lines string) string {
	return checkoutWithWallClock("20m") + "  max_diff_lines: " + lines + "\n"
}

// A head that changes more lines than the contract allows never leaves the
// control plane: the run fails diff_too_large, both readers and the
// evidence show the count against the limit, and the issue is freed.
func TestImplement_diffOverLimit_noPushNoPRNoStatus_failedDiffTooLarge(t *testing.T) {
	t.Parallel()
	fx := limitsFixture(t, checkoutWithMaxDiff("100"), nil)
	fx.cloner.co.diffLines = 101

	res := callImplement(t, fx.cs, implementIssue)
	out := decodeImplement(t, res)
	if got, want := fx.calls.list(), []string{"claim", "put running", "clone", "agent", "gates", "release", "put failed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q, want %q: no push, pull request or status", got, want)
	}
	for reader, wire := range map[string]map[string]json.RawMessage{"output": wireOf(t, res), "status": statusOf(t, fx)} {
		if string(wire["state"]) != `"failed"` || string(wire["failure_reason"]) != `"diff_too_large"` ||
			string(wire["diff_lines"]) != "101" || string(wire["max_diff_lines"]) != "100" {
			t.Errorf("%s state %s reason %s diff_lines %s max_diff_lines %s, want failed diff_too_large 101 over 100",
				reader, wire["state"], wire["failure_reason"], wire["diff_lines"], wire["max_diff_lines"])
		}
	}
	if !strings.Contains(out.Evidence, "| Diff lines (measured / limit) | 101 / 100 |") {
		t.Fatalf("evidence does not show the count against the limit:\n%s", out.Evidence)
	}
	if released := fx.claims.releasedClaims(); len(released) != 1 {
		t.Fatalf("released %d claims, want 1", len(released))
	}
	assertFailedAt(t, fx, "diff")
}

// The limit is the most the head may change: at it the run delivers, one
// line over it does not, whether the limit is the contract's or the
// default.
func TestImplement_diffAtLimit_delivered(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		checkout   string
		lines      int
		wantState  string
		wantReason string
	}{
		{name: "at the contract's limit", checkout: checkoutWithMaxDiff("100"), lines: 100, wantState: "succeeded"},
		{name: "over the contract's limit", checkout: checkoutWithMaxDiff("100"), lines: 101, wantState: "failed", wantReason: "diff_too_large"},
		{name: "at the default limit", checkout: checkoutWithWallClock("20m"), lines: 400, wantState: "succeeded"},
		{name: "over the default limit", checkout: checkoutWithWallClock("20m"), lines: 401, wantState: "failed", wantReason: "diff_too_large"},
		{name: "no line changed", checkout: checkoutWithWallClock("20m"), lines: 0, wantState: "succeeded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fx := limitsFixture(t, tt.checkout, nil)
			fx.cloner.co.diffLines = tt.lines

			out := decodeImplement(t, callImplement(t, fx.cs, implementIssue))
			if out.State != tt.wantState || out.FailureReason != tt.wantReason {
				t.Fatalf("state %q reason %q, want %q %q", out.State, out.FailureReason, tt.wantState, tt.wantReason)
			}
			if pushed := len(fx.pusher.got()) == 1; pushed != (tt.wantState == "succeeded") {
				t.Fatalf("pushed %d times, want a push only for a delivered run", len(fx.pusher.got()))
			}
		})
	}
}

// A red gate stops the run before its size is judged: the reason is the
// gate, and the count still shows.
func TestImplement_gateRedAndDiffOver_reasonGateFailed(t *testing.T) {
	t.Parallel()
	fx := limitsFixture(t, checkoutWithMaxDiff("100"), nil)
	fx.cloner.co.diffLines = 101
	fx.gates.report = redReport()

	out := decodeImplement(t, callImplement(t, fx.cs, implementIssue))
	if out.State != "failed" || out.FailureReason != "gate_failed" || out.DiffLines == nil || *out.DiffLines != 101 {
		t.Fatalf("state %q reason %q diff_lines %v, want failed gate_failed with 101 counted", out.State, out.FailureReason, out.DiffLines)
	}
	assertFailedAt(t, fx, "gates")
}

// A head whose size cannot be counted is not judged under the limit: the
// run fails as a tool error before any gate, and the issue is freed.
func TestImplement_diffUnmeasurable_toolErrorRunFailed(t *testing.T) {
	t.Parallel()
	fx := limitsFixture(t, checkoutWithMaxDiff("100"), nil)
	fx.cloner.co.diffErr = errors.New("git diff died")

	res := callImplement(t, fx.cs, implementIssue)
	if !res.IsError || !strings.Contains(textOf(res), "git diff died") {
		t.Fatalf("IsError %v text %q, want the count's failure as a tool error", res.IsError, textOf(res))
	}
	if fx.gates.calls != 0 || len(fx.pusher.got()) != 0 {
		t.Fatalf("gates ran %d times, pushes %d, want none", fx.gates.calls, len(fx.pusher.got()))
	}
	assertFailedAt(t, fx, "agent")
	if wire := statusOf(t, fx); string(wire["state"]) != `"failed"` {
		t.Fatalf("status state %s, want failed", wire["state"])
	}
	if released := fx.claims.releasedClaims(); len(released) != 1 {
		t.Fatalf("released %d claims, want 1", len(released))
	}
}

// A run that failed before it read its contract worked under no limits:
// forja.status leaves them out rather than show the defaults.
func TestImplement_cloneFails_limitsAbsentFromStatus(t *testing.T) {
	t.Parallel()
	fx := limitsFixture(t, checkoutWithMaxDiff("100"), nil)
	fx.cloner.err = errors.New("clone refused")

	if res := callImplement(t, fx.cs, implementIssue); !res.IsError {
		t.Fatalf("IsError false, want the clone's failure as a tool error")
	}
	wire := statusOf(t, fx)
	for _, field := range []string{"wall_clock_seconds", "max_diff_lines", "diff_lines"} {
		if raw, present := wire[field]; present {
			t.Errorf("status %s = %s for a run that never read its contract", field, raw)
		}
	}
}

// A run that never counted its diff says so by leaving diff_lines out,
// never with a zero.
func TestImplement_noCommits_diffLinesAbsent(t *testing.T) {
	t.Parallel()
	fx := limitsFixture(t, checkoutWithMaxDiff("100"), nil)
	fx.agent.result = agent.Result{ExitCode: 0, Tokens: spent}

	output := wireOf(t, callImplement(t, fx.cs, implementIssue))
	for reader, wire := range map[string]map[string]json.RawMessage{"output": output, "status": statusOf(t, fx)} {
		if raw, present := wire["diff_lines"]; present {
			t.Errorf("%s diff_lines = %s for a run that counted none", reader, raw)
		}
	}
}
