package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/agent"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
	forja "github.com/fuseone-io/fuseone-forja/internal/mcp"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

// testPrices is an operator's table with a price of its own per class.
var testPrices = domain.PriceTable{Input: 2_000_000, CacheWrite: 2_500_000, CacheRead: 200_000, Output: 10_000_000, ComputeMinute: 40_000}

// What each stage's runner measured. Neither is a whole number of
// thousandths of a minute, so the wire's rounding shows.
const (
	agentCompute = 4*time.Minute + 31234*time.Millisecond
	gatesCompute = 2*time.Minute + 15500*time.Millisecond
)

// The wire figures of those measures and of spent at testPrices. The
// tokens of spent cost (1000·2 + 200·2.5 + 30·0.2 + 400·10) = 6 506
// micros; a minute 40 000.
const (
	agentMinutes = "4.521" // 271.234s
	gatesMinutes = "2.258" // 135.5s
	runMinutes   = "6.779" // 406.734s
	// 271 234 ms · 40 000 / 60 000 = 180 822, plus the tokens.
	agentOnlyCost = "187328"
	// 406 734 ms · 40 000 / 60 000 = 271 156, plus the tokens.
	runCost = "277662"
)

// charged is r with the agent's measured compute.
func charged(r agent.Result) agent.Result {
	r.Compute = agentCompute
	return r
}

// pricedFixture is the implement fixture with the price table, an agent
// answering r and gates that measured gatesCompute.
func pricedFixture(t *testing.T, r agent.Result) *implementFixture {
	t.Helper()
	fx := newWiredFixture(t, contractWithWallClock, fixtureWiring{
		agent: &fakeAgent{result: r},
		deps:  func(d *forja.Deps) { d.Prices = &testPrices },
	})
	fx.gates.report.Compute = gatesCompute
	return fx
}

func implementWire(t *testing.T, fx *implementFixture) map[string]json.RawMessage {
	t.Helper()
	res := callImplement(t, fx.cs, implementIssue)
	if res.IsError {
		t.Fatalf("IsError = true, content: %s", textOf(res))
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(structuredJSON(t, res), &wire); err != nil {
		t.Fatalf("decode wire object: %v", err)
	}
	return wire
}

// assertCost checks the four cost fields of a wire object.
func assertCost(t *testing.T, what string, wire map[string]json.RawMessage, minutes, cost string) {
	t.Helper()
	got := [4]string{string(wire["compute_minutes"]), string(wire["tokens_in"]), string(wire["tokens_out"]), string(wire["estimated_cost_usd_micros"])}
	if want := [4]string{minutes, "1230", "400", cost}; got != want {
		t.Fatalf("%s compute_minutes, tokens_in, tokens_out, estimated_cost_usd_micros = %q, want %q", what, got, want)
	}
}

// The compute is both stages', the agent's workspace and the gates', and
// the estimate prices it with the agent's tokens.
func TestImplement_greenRun_returnsComputeTokensAndEstimate(t *testing.T) {
	t.Parallel()
	fx := pricedFixture(t, charged(exported()))

	wire := implementWire(t, fx)
	if string(wire["state"]) != `"succeeded"` {
		t.Fatalf("state = %s, want succeeded", wire["state"])
	}
	assertCost(t, "forja.implement", wire, runMinutes, runCost)
}

// A server with no price table has no estimate to give: the key is absent,
// never a zero that reads as free; the compute is still there.
func TestImplement_noPriceTable_estimateAbsent(t *testing.T) {
	t.Parallel()
	fx := newAgentFixture(t, contractWithWallClock, nil, &fakeAgent{result: charged(exported())})
	fx.gates.report.Compute = gatesCompute

	wire := implementWire(t, fx)
	if raw, present := wire["estimated_cost_usd_micros"]; present {
		t.Fatalf("estimated_cost_usd_micros = %s on a server with no price table, want the key absent", raw)
	}
	if string(wire["compute_minutes"]) != runMinutes {
		t.Fatalf("compute_minutes = %s, want %s", wire["compute_minutes"], runMinutes)
	}
	if status := statusOf(t, fx); status["estimated_cost_usd_micros"] != nil || string(status["compute_minutes"]) != runMinutes {
		t.Fatalf("status estimate %s compute %s, want no estimate and %s", status["estimated_cost_usd_micros"], status["compute_minutes"], runMinutes)
	}
}

// A run whose runners measured nothing still answers its compute: the key
// is on the wire as 0, never absent.
func TestImplement_nothingMeasured_computeMinutesZeroOnWire(t *testing.T) {
	t.Parallel()
	fx := newAgentFixture(t, contractWithWallClock, nil, &fakeAgent{result: exported()})

	if raw := implementWire(t, fx)["compute_minutes"]; string(raw) != "0" {
		t.Fatalf("compute_minutes = %q, want 0 on the wire", raw)
	}
}

// On a priced server a run that used nothing is estimated at 0, on the
// wire: an absent key is a server with no price table.
func TestImplement_pricedNothingUsed_estimateZeroOnWire(t *testing.T) {
	t.Parallel()
	fx := pricedFixture(t, agent.Result{ExitCode: 0, Exported: true})
	fx.gates.report.Compute = 0

	implemented, status := implementWire(t, fx), statusOf(t, fx)
	if got := [2]string{string(implemented["estimated_cost_usd_micros"]), string(status["estimated_cost_usd_micros"])}; got != [2]string{"0", "0"} {
		t.Fatalf("estimated_cost_usd_micros on implement and status = %q, want 0 on both", got)
	}
}

// stageWire is each stage of a status wire object by name, as its raw keys.
func stageWire(t *testing.T, wire map[string]json.RawMessage) map[string]map[string]json.RawMessage {
	t.Helper()
	var list []map[string]json.RawMessage
	if err := json.Unmarshal(wire["stages"], &list); err != nil {
		t.Fatalf("stages = %s: %v", wire["stages"], err)
	}
	out := map[string]map[string]json.RawMessage{}
	for _, s := range list {
		var name string
		if err := json.Unmarshal(s["name"], &name); err != nil {
			t.Fatalf("stage name %s: %v", s["name"], err)
		}
		out[name] = s
	}
	return out
}

// assertStageCost checks one stage's three cost fields, all on the wire.
func assertStageCost(t *testing.T, stages map[string]map[string]json.RawMessage, name, minutes, in, out string) {
	t.Helper()
	s := stages[name]
	got := [3]string{string(s["compute_minutes"]), string(s["tokens_in"]), string(s["tokens_out"])}
	if want := [3]string{minutes, in, out}; got != want {
		t.Fatalf("stage %s compute_minutes, tokens_in, tokens_out = %q, want %q", name, got, want)
	}
}

// While the gates run, the agent stage has ended and recorded its cost;
// the running gates stage shows zeros until it ends.
func TestStatus_runInGatesStage_reportsAgentStageCostOnly(t *testing.T) {
	t.Parallel()
	fx := pricedFixture(t, charged(exported()))
	var midRun map[string]json.RawMessage
	fx.gates.during = func() { midRun = statusOf(t, fx) }

	implementWire(t, fx)
	assertCost(t, "forja.status during the gates", midRun, agentMinutes, agentOnlyCost)
	stages := stageWire(t, midRun)
	assertStageCost(t, stages, "agent", agentMinutes, "1230", "400")
	assertStageCost(t, stages, "gates", "0", "0", "0")
	assertStageCost(t, stages, "clone", "0", "0", "0")
}

// Once the run ended, its totals are the sum of its stages', and both
// readers, forja.implement and forja.status, give the same figures.
func TestStatus_finishedRun_perStageAndTotalsAgree(t *testing.T) {
	t.Parallel()
	fx := pricedFixture(t, charged(exported()))

	implemented := implementWire(t, fx)
	status := statusOf(t, fx)
	assertCost(t, "forja.status", status, runMinutes, runCost)
	assertCost(t, "forja.implement", implemented, runMinutes, runCost)
	stages := stageWire(t, status)
	assertStageCost(t, stages, "agent", agentMinutes, "1230", "400")
	assertStageCost(t, stages, "gates", gatesMinutes, "0", "0")
	for _, name := range []string{"claim", "clone", "push", "pull_request", "status", "release"} {
		assertStageCost(t, stages, name, "0", "0", "0")
	}
}

// A doctor run records no stages but its gates workspace was up: the
// compute is the run's on every outcome, a runner error included.
func TestStatus_doctorRun_reportsCompute(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		gates *fakeGates
	}{
		{name: "green", gates: &fakeGates{report: greenReport()}},
		{name: "red", gates: &fakeGates{report: redReport()}},
		{name: "runner error", gates: &fakeGates{report: domain.GateReport{}, err: errors.New("daemon gone")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.gates.report.Compute = gatesCompute
			store := runstore.NewMemory()
			deps := testDeps(store, &fakeContracts{}, &fakeClaims{calls: &callLog{}})
			deps.Cloner, deps.Gates, deps.Prices = &fakeCloner{co: doctorCheckout(doctorContract)}, tt.gates, &testPrices
			cs := connectDeps(t, deps)

			if res := callDoctor(t, cs, doctorRepo); res.IsError != (tt.gates.err != nil) {
				t.Fatalf("doctor IsError = %v: %s", res.IsError, textOf(res))
			}
			wire := statusWire(t, cs, "run-1")
			// 135 500 ms · 40 000 / 60 000 = 90 333; no tokens.
			if string(wire["compute_minutes"]) != gatesMinutes || string(wire["estimated_cost_usd_micros"]) != "90333" {
				t.Fatalf("compute_minutes %s estimate %s, want %s and 90333", wire["compute_minutes"], wire["estimated_cost_usd_micros"], gatesMinutes)
			}
		})
	}
}

// A run stopped by the cap or the deadline is a result, and it carries
// what the agent's workspace used.
func TestImplement_tokenCap_reportsCost(t *testing.T) {
	t.Parallel()
	fx := pricedFixture(t, charged(agent.Result{ExitCode: domain.NoExitCode, Capped: true, Tokens: spent}))

	wire := implementWire(t, fx)
	if string(wire["failure_reason"]) != `"token_cap"` {
		t.Fatalf("failure_reason = %s, want token_cap", wire["failure_reason"])
	}
	assertCost(t, "forja.implement", wire, agentMinutes, agentOnlyCost)
}

func TestImplement_agentTimedOut_reportsCost(t *testing.T) {
	t.Parallel()
	fx := pricedFixture(t, charged(agent.Result{ExitCode: domain.NoExitCode, TimedOut: true, Tokens: spent}))

	wire := implementWire(t, fx)
	if string(wire["state"]) != `"failed"` {
		t.Fatalf("state = %s, want failed", wire["state"])
	}
	assertCost(t, "forja.implement", wire, agentMinutes, agentOnlyCost)
}

func TestImplement_redGate_reportsCost(t *testing.T) {
	t.Parallel()
	fx := pricedFixture(t, charged(exported()))
	fx.gates.report = redReport()
	fx.gates.report.Compute = gatesCompute

	wire := implementWire(t, fx)
	if string(wire["failure_reason"]) != `"gate_failed"` {
		t.Fatalf("failure_reason = %s, want gate_failed", wire["failure_reason"])
	}
	assertCost(t, "forja.implement", wire, runMinutes, runCost)
}

// A gates runner that failed still had its workspace up: the tool error
// goes back, and the run it recorded holds both stages' compute.
func TestImplement_gatesError_computeRecordedOnRun(t *testing.T) {
	t.Parallel()
	fx := pricedFixture(t, charged(exported()))
	fx.gates.err = errors.New("daemon gone")

	if res := callImplement(t, fx.cs, implementIssue); !res.IsError || !strings.Contains(textOf(res), "daemon gone") {
		t.Fatalf("IsError %v text %q, want the gates failure as a tool error", res.IsError, textOf(res))
	}
	run, err := fx.runs.Get(context.Background(), "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if run.Compute != agentCompute+gatesCompute {
		t.Fatalf("run compute = %v, want the agent's %v and the gates' %v", run.Compute, agentCompute, gatesCompute)
	}
	for _, s := range run.Stages {
		if s.Name == domain.StageGates && (s.Compute != gatesCompute || s.Outcome != domain.StageFailed) {
			t.Fatalf("gates stage = %+v, want failed with %v", s, gatesCompute)
		}
	}
}

// An agent runner that failed still had its workspace up: the tool error
// goes back, and forja.status reads the agent's compute and its estimate.
func TestImplement_agentError_costOnStatus(t *testing.T) {
	t.Parallel()
	failing := &fakeAgent{result: charged(agent.Result{ExitCode: domain.NoExitCode, Tokens: spent}), err: errors.New("daemon gone")}
	fx := newWiredFixture(t, contractWithWallClock, fixtureWiring{
		agent: failing,
		deps:  func(d *forja.Deps) { d.Prices = &testPrices },
	})

	if res := callImplement(t, fx.cs, implementIssue); !res.IsError || !strings.Contains(textOf(res), "daemon gone") {
		t.Fatalf("IsError %v text %q, want the agent failure as a tool error", res.IsError, textOf(res))
	}
	status := statusOf(t, fx)
	assertCost(t, "forja.status", status, agentMinutes, agentOnlyCost)
	assertStageCost(t, stageWire(t, status), "agent", agentMinutes, "1230", "400")
}

// A delivery that failed after both workspaces ran is a tool error; what
// they cost is on the run forja.status reads.
func TestImplement_pushFails_reportsCostOfAgentAndGates(t *testing.T) {
	t.Parallel()
	fx := pricedFixture(t, charged(exported()))
	fx.pusher.err = errors.New("push refused")

	if res := callImplement(t, fx.cs, implementIssue); !res.IsError {
		t.Fatalf("IsError = false when the push fails: %s", textOf(res))
	}
	assertCost(t, "forja.status", statusOf(t, fx), runMinutes, runCost)
}
