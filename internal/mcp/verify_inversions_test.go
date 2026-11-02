package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/agent"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/github"
	forja "github.com/fuseone-io/fuseone-forja/internal/mcp"
)

const (
	bitesDescription = "Bites: every inversion turned a gate red."
	// coveredBody is what the developer declared on the pull request.
	coveredBody = "Tested the estimate with a table of prices.\n"
)

// fakeQA is the QA agent: it records the task and the deadline it ran
// under, and, as the real runner does, writes the bundle of each candidate
// it reports exported, reports one entry per candidate, and none when the
// cap stopped it.
type fakeQA struct {
	calls  *callLog
	result agent.QAResult
	err    error
	// run, when set, answers in place of result and err under the run's
	// context.
	run func(ctx context.Context) (agent.QAResult, error)

	mu       sync.Mutex
	tasks    []agent.QATask
	deadline time.Time
}

func (f *fakeQA) RunQA(ctx context.Context, t agent.QATask) (agent.QAResult, error) {
	if f.calls != nil {
		f.calls.add("agent")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tasks = append(f.tasks, t)
	f.deadline, _ = ctx.Deadline()
	res, err := f.result, f.err
	if f.run != nil {
		res, err = f.run(ctx)
	}
	exported := make([]bool, len(t.Candidates))
	copy(exported, res.Exported)
	if res.Capped {
		clear(exported)
	}
	for i, ok := range exported {
		if !ok {
			continue
		}
		if werr := os.WriteFile(t.Candidates[i].Bundle, []byte("# v2 git bundle\n"), 0o600); werr != nil {
			return agent.QAResult{ExitCode: domain.NoExitCode}, werr
		}
	}
	res.Exported = exported
	return res, err
}

func (f *fakeQA) taskList() []agent.QATask {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agent.QATask(nil), f.tasks...)
}

// coveredPull changes two production files of the pilot and adds a test,
// with the developer's declaration as its body.
func coveredPull() domain.PullRequestDiff {
	pr := verifyPull(changed("internal/domain/cost.go", "internal/mcp/cost.go", "internal/domain/cost_test.go")...)
	pr.Body = coveredBody
	return pr
}

func candidateBranch(k int) string { return "forja-qa/" + strconv.Itoa(k) }
func candidateHead(k int) string   { return fmt.Sprintf("%040x", 0xc0ffee00+k) }
func candidateTree(k int) string   { return "/tmp/forja-verify@" + candidateBranch(k) }
func patchOf(k int) string         { return fmt.Sprintf("-\treturn a / 60_000\n+\treturn a / %d_000\n", k) }

// candidate is one inversion the QA agent leaves: what it changes against
// the head, what is left changed against the base, and the report its
// gates answer. empty leaves its branch at the head.
type candidate struct {
	paths    []string
	fromBase []string
	report   domain.GateReport
	empty    bool
}

func redAtGate(name string) domain.GateReport {
	return domain.GateReport{
		Setup: []domain.GateResult{passed("setup[0]", "go mod download", "", "", time.Second)},
		Gates: []domain.GateResult{
			passed("build", "make build", "", "", time.Second),
			{Name: name, Command: "make " + name, Status: domain.StepFailed, ExitCode: 2, Stderr: []byte("FAIL\n")},
		},
		FailedStep: name,
	}
}

func bitAt(gate string) candidate { return candidate{report: redAtGate(gate)} }
func escapes() candidate          { return candidate{report: greenReport()} }
func touching(paths ...string) candidate {
	return candidate{paths: paths, report: redAtGate("test")}
}

// invert has the QA agent exit 0 having exported every candidate, in
// order, and scripts the checkout and the gates for each.
func invert(fx *verifyFixture, cands ...candidate) {
	co := fx.cloner.co
	co.candidates, co.changeSets = map[string]string{}, map[[2]string]domain.Change{}
	if fx.gates.byTree == nil {
		fx.gates.byTree = map[string]domain.GateReport{}
	}
	exported := make([]bool, len(cands))
	for i, c := range cands {
		k := i + 1
		head := candidateHead(k)
		if c.empty {
			head = verifyHead
		}
		paths, fromBase := c.paths, c.fromBase
		if paths == nil {
			paths = []string{"internal/domain/cost.go"}
		}
		if fromBase == nil {
			fromBase = []string{"internal/domain/cost_test.go", "internal/mcp/cost.go"}
		}
		co.candidates[candidateBranch(k)] = head
		co.changeSets[[2]string{verifyHead, head}] = domain.Change{Paths: paths, Patch: patchOf(k)}
		co.changeSets[[2]string{planSHA, head}] = domain.Change{Paths: fromBase}
		fx.gates.byTree[candidateTree(k)] = c.report
		exported[i] = true
	}
	fx.qa.result = agent.QAResult{ExitCode: 0, Exported: exported, Tokens: spent}
}

// inversionWire is one inversion as forja.verify answers it.
type inversionWire struct {
	Branch     string   `json:"branch"`
	Commit     string   `json:"commit"`
	Paths      []string `json:"paths"`
	Admitted   bool     `json:"admitted"`
	Rejected   string   `json:"rejected"`
	Outcome    string   `json:"outcome"`
	FailedStep string   `json:"failed_step"`
}

func inversionsOf(t *testing.T, wire map[string]json.RawMessage) []inversionWire {
	t.Helper()
	var out []inversionWire
	if err := json.Unmarshal(wire["inversions"], &out); err != nil {
		t.Fatalf("inversions = %s: %v", wire["inversions"], err)
	}
	return out
}

func stageNames(t *testing.T, fx *verifyFixture) []string {
	t.Helper()
	var names []string
	for _, s := range stagesOf(t, statusWire(t, fx.cs, "run-1")) {
		names = append(names, s.Name)
	}
	return names
}

func assertNoStatus(t *testing.T, fx *verifyFixture) {
	t.Helper()
	if got := fx.statuses.got(); len(got) != 0 {
		t.Fatalf("statuses = %+v, want none", got)
	}
}

// AC1: a covered pull request bites. Each admitted inversion runs the
// base contract's gates on its own tree and goes red at test; forja/qa is
// success on the head, and the evidence carries both patches and both red
// gates.
func TestVerify_everyInversionRed_bitesAndQASuccess(t *testing.T) {
	t.Parallel()
	fx := newVerifyFixture(t, verifyWiring{pr: coveredPull(), fakes: func(fx *verifyFixture) {
		invert(fx, bitAt("test"), touching("internal/mcp/cost.go"))
	}})

	res := callVerify(t, fx.cs, verifyPR)
	out, wire := decodeVerify(t, res), wireOf(t, res)
	assertOneStatus(t, fx, github.StateSuccess, bitesDescription)
	if out.State != "succeeded" || out.Verdict != "bites" || out.FailureReason != "" {
		t.Fatalf("state %q verdict %q reason %q, want succeeded bites", out.State, out.Verdict, out.FailureReason)
	}
	assertInversions(t, wire, []inversionWire{
		{Branch: "forja-qa/1", Commit: candidateHead(1), Paths: []string{"internal/domain/cost.go"}, Admitted: true, Outcome: "bit", FailedStep: "test"},
		{Branch: "forja-qa/2", Commit: candidateHead(2), Paths: []string{"internal/mcp/cost.go"}, Admitted: true, Outcome: "bit", FailedStep: "test"},
	})
	assertBothBitInEvidence(t, out.Evidence)
	assertGatesOnTreesUnderBaseContract(t, fx, "/tmp/forja-verify", candidateTree(1), candidateTree(2))
	if got, want := stageNames(t, fx), []string{"claim", "clone", "diff", "gates", "agent", "inversion", "inversion", "status", "release"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stages = %q, want %q", got, want)
	}
}

// assertOneStatus checks exactly one forja/qa was published, on the head,
// pointing at the pull request, with state and description.
func assertOneStatus(t *testing.T, fx *verifyFixture, state, description string) {
	t.Helper()
	want := github.StatusRequest{Repository: doctorRepo, SHA: verifyHead, State: state, Context: "forja/qa", Description: description, TargetURL: verifyPRURL}
	if got := fx.statuses.got(); len(got) != 1 || got[0] != want {
		t.Fatalf("statuses = %+v, want [%+v]", got, want)
	}
}

func assertInversions(t *testing.T, wire map[string]json.RawMessage, want []inversionWire) {
	t.Helper()
	if got := inversionsOf(t, wire); !reflect.DeepEqual(got, want) {
		t.Fatalf("inversions = %+v, want %+v", got, want)
	}
}

func assertBothBitInEvidence(t *testing.T, evidence string) {
	t.Helper()
	for _, part := range []string{patchOf(1), patchOf(2), "| Baseline gates on the head | passed |"} {
		if !strings.Contains(evidence, part) {
			t.Errorf("evidence lacks %q:\n%s", part, evidence)
		}
	}
	if n := strings.Count(evidence, "| Red gate | `test` |"); n != 2 {
		t.Errorf("evidence names %d red gates, want 2:\n%s", n, evidence)
	}
}

// assertGatesOnTreesUnderBaseContract checks the gates ran once per tree,
// in order, each under the base checkout's contract and never the API's
// or a candidate's.
func assertGatesOnTreesUnderBaseContract(t *testing.T, fx *verifyFixture, trees ...string) {
	t.Helper()
	if !reflect.DeepEqual(fx.gates.trees, trees) {
		t.Fatalf("gates ran on %q, want %q", fx.gates.trees, trees)
	}
	for _, c := range fx.gates.contracts {
		if c.Image != "golang:1.26" || len(c.Gates) != 1 || c.Gates[0].Run != "make build" {
			t.Fatalf("gates ran under %+v, want the base checkout's contract", c)
		}
	}
}

// AC2: an uncovered pull request does not bite. An inversion that passed
// every gate is named on forja/qa failure, and the evidence marks it.
func TestVerify_oneInversionGreen_doesNotBiteNamesEscaped(t *testing.T) {
	t.Parallel()
	fx := newVerifyFixture(t, verifyWiring{pr: coveredPull(), fakes: func(fx *verifyFixture) {
		invert(fx, bitAt("test"), escapes())
	}})

	res := callVerify(t, fx.cs, verifyPR)
	out, wire := decodeVerify(t, res), wireOf(t, res)
	assertOneStatus(t, fx, github.StateFailure, "Does not bite: inversion forja-qa/2 passed every gate.")
	if out.State != "succeeded" || out.Verdict != "does_not_bite" {
		t.Fatalf("state %q verdict %q, want succeeded does_not_bite", out.State, out.Verdict)
	}
	assertInversions(t, wire, []inversionWire{
		{Branch: "forja-qa/1", Commit: candidateHead(1), Paths: []string{"internal/domain/cost.go"}, Admitted: true, Outcome: "bit", FailedStep: "test"},
		{Branch: "forja-qa/2", Commit: candidateHead(2), Paths: []string{"internal/domain/cost.go"}, Admitted: true, Outcome: "escaped"},
	})
	inOrderIn(t, out.Evidence, "### `forja-qa/2`", "| Outcome | `escaped` |", "| Red gate | — |")
}

// Escaped whatever the order: the first that passed is the one named.
func TestVerify_firstInversionGreen_namedOnFailure(t *testing.T) {
	t.Parallel()
	fx := newVerifyFixture(t, verifyWiring{pr: coveredPull(), fakes: func(fx *verifyFixture) {
		invert(fx, escapes(), escapes())
	}})

	decodeVerify(t, callVerify(t, fx.cs, verifyPR))
	if got := fx.statuses.got(); len(got) != 1 || got[0].Description != "Does not bite: inversion forja-qa/1 passed every gate." {
		t.Fatalf("statuses = %+v, want forja-qa/1 named", got)
	}
}

func inOrderIn(t *testing.T, s string, parts ...string) {
	t.Helper()
	from := 0
	for _, p := range parts {
		i := strings.Index(s[from:], p)
		if i < 0 {
			t.Fatalf("lacks %q after offset %d:\n%s", p, from, s)
		}
		from += i + len(p)
	}
}

// AC3: a candidate that touches a test is refused and kept with its
// reason; the next two run, and only they and the baseline reach the
// gates.
func TestVerify_candidateTouchesTest_rejectedNextAdmitted(t *testing.T) {
	t.Parallel()
	fx := newVerifyFixture(t, verifyWiring{pr: coveredPull(), fakes: func(fx *verifyFixture) {
		invert(fx, touching("internal/domain/cost.go", "internal/domain/x_test.go"), bitAt("test"), bitAt("vet"))
	}})

	res := callVerify(t, fx.cs, verifyPR)
	out, wire := decodeVerify(t, res), wireOf(t, res)
	assertInversions(t, wire, []inversionWire{
		{Branch: "forja-qa/1", Commit: candidateHead(1), Paths: []string{"internal/domain/cost.go", "internal/domain/x_test.go"}, Rejected: "touches_non_production"},
		{Branch: "forja-qa/2", Commit: candidateHead(2), Paths: []string{"internal/domain/cost.go"}, Admitted: true, Outcome: "bit", FailedStep: "test"},
		{Branch: "forja-qa/3", Commit: candidateHead(3), Paths: []string{"internal/domain/cost.go"}, Admitted: true, Outcome: "bit", FailedStep: "vet"},
	})
	assertGatesOnTreesUnderBaseContract(t, fx, "/tmp/forja-verify", candidateTree(2), candidateTree(3))
	if out.Verdict != "bites" || !strings.Contains(out.Evidence, "rejected: `touches_non_production`") {
		t.Fatalf("verdict %q; evidence:\n%s", out.Verdict, out.Evidence)
	}
}

// Two admitted inversions are enough: a candidate after them is never
// looked at.
func TestVerify_moreThanTwoAdmissible_firstTwoRun(t *testing.T) {
	t.Parallel()
	fx := newVerifyFixture(t, verifyWiring{pr: coveredPull(), fakes: func(fx *verifyFixture) {
		invert(fx, bitAt("test"), bitAt("test"), escapes(), escapes())
	}})

	wire := wireOf(t, callVerify(t, fx.cs, verifyPR))
	if inv := inversionsOf(t, wire); len(inv) != 2 {
		t.Fatalf("inversions = %+v, want the first two", inv)
	}
	assertGatesOnTreesUnderBaseContract(t, fx, "/tmp/forja-verify", candidateTree(1), candidateTree(2))
	if got := fx.statuses.got(); len(got) != 1 || got[0].State != github.StateSuccess {
		t.Fatalf("statuses = %+v, want success from the first two", got)
	}
}

// No admissible candidate is no verdict: the run fails, publishes nothing
// and keeps every refusal with its reason.
func TestVerify_noAdmissibleCandidate_failsInversionUnavailableNoStatus(t *testing.T) {
	t.Parallel()
	fx := newVerifyFixture(t, verifyWiring{pr: coveredPull(), fakes: func(fx *verifyFixture) {
		invert(fx,
			touching("internal/domain/cost_test.go"),
			touching("internal/gates/runner.go"),
			candidate{empty: true},
			candidate{fromBase: []string{"internal/domain/cost_test.go"}},
		)
	}})

	res := callVerify(t, fx.cs, verifyPR)
	wire := wireOf(t, res)
	assertNoStatus(t, fx)
	if string(wire["state"]) != `"failed"` || string(wire["failure_reason"]) != `"inversion_unavailable"` {
		t.Fatalf("state %s reason %s, want failed inversion_unavailable", wire["state"], wire["failure_reason"])
	}
	var reasons []string
	for _, inv := range inversionsOf(t, wire) {
		reasons = append(reasons, inv.Rejected)
	}
	if want := []string{"touches_non_production", "outside_the_change", "empty", "reverts_whole_diff"}; !reflect.DeepEqual(reasons, want) {
		t.Fatalf("rejections = %q, want %q", reasons, want)
	}
	if fx.gates.calls != 1 {
		t.Fatalf("gates ran %d times, want the baseline only", fx.gates.calls)
	}
	assertVerifyFailedAt(t, fx, "agent")
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(wire["inversions"], &raw); err != nil || len(raw) != 4 {
		t.Fatalf("inversions = %s, %v", wire["inversions"], err)
	}
	if _, commit := raw[2]["commit"]; string(raw[2]["paths"]) != "[]" || commit {
		t.Fatalf("empty candidate = %s, want paths [] and no commit", mustJSON(t, raw[2]))
	}
}

// A gate result that is neither red at a gate nor all green proves
// nothing: the run fails with no verdict and no status.
func TestVerify_inversionSetupRed_inconclusiveNoStatus(t *testing.T) {
	t.Parallel()
	setupRed := domain.GateReport{
		Setup:      []domain.GateResult{{Name: "setup[0]", Command: "go mod download", Status: domain.StepFailed, ExitCode: 1}},
		Gates:      []domain.GateResult{skippedStep("build", "make build")},
		FailedStep: "setup[0]",
	}
	fx := newVerifyFixture(t, verifyWiring{pr: coveredPull(), fakes: func(fx *verifyFixture) {
		invert(fx, bitAt("test"), candidate{report: setupRed})
	}})

	out := decodeVerify(t, callVerify(t, fx.cs, verifyPR))
	assertNoStatus(t, fx)
	if out.State != "failed" || out.FailureReason != "inversion_inconclusive" || out.Verdict != "" {
		t.Fatalf("state %q reason %q verdict %q, want failed inversion_inconclusive with no verdict", out.State, out.FailureReason, out.Verdict)
	}
	if fx.gates.calls != 3 {
		t.Fatalf("gates ran %d times, want the baseline and both inversions", fx.gates.calls)
	}
}

// AC4: the QA agent's tokens and every gates run's compute are the verify
// run's, on its agent stage and its gates and inversion stages, priced
// with the operator's table; the implement run beside it is untouched.
func TestVerify_qaAgent_costOnVerifyRunOnly(t *testing.T) {
	t.Parallel()
	fx := newVerifyFixture(t, verifyWiring{pr: coveredPull(), fakes: func(fx *verifyFixture) {
		invert(fx, bitAt("test"), bitAt("test"))
		fx.qa.result.Compute = 2 * time.Minute
		fx.gates.report.Compute = 30 * time.Second
		for tree, r := range fx.gates.byTree {
			r.Compute = 30 * time.Second
			fx.gates.byTree[tree] = r
		}
	}, deps: func(d *forja.Deps) { d.Prices = &testPrices }})
	implement := terminalRun("run-implement")
	implement.Tokens, implement.Compute = domain.TokenUsage{Input: 9, Output: 9}, time.Hour
	mustPut(t, fx.runs.Memory, implement)

	wire := wireOf(t, callVerify(t, fx.cs, verifyPR))
	// spent at testPrices is 6506 micros; 3.5 minutes at 40 000 a minute 140 000.
	want := map[string]string{"tokens_in": "1230", "tokens_out": "400", "compute_minutes": "3.5", "estimated_cost_usd_micros": "146506"}
	for key, value := range want {
		if string(wire[key]) != value {
			t.Errorf("%s = %s, want %s", key, wire[key], value)
		}
	}
	assertVerifyStageCost(t, stagesOf(t, statusWire(t, fx.cs, "run-1")))
	if got, err := fx.runs.Get(context.Background(), "run-implement"); err != nil || !reflect.DeepEqual(got, implement) {
		t.Fatalf("implement run = %+v, %v; want it unchanged", got, err)
	}
}

func assertVerifyStageCost(t *testing.T, stages []forja.StageReport) {
	t.Helper()
	for _, s := range stages {
		wantIn, wantMinutes := int64(0), 0.0
		switch s.Name {
		case "agent":
			wantIn, wantMinutes = 1230, 2
		case "gates", "inversion":
			wantMinutes = 0.5
		}
		if s.TokensIn != wantIn || s.ComputeMinutes != wantMinutes {
			t.Errorf("stage %s: tokens_in %d compute %v, want %d and %v", s.Name, s.TokensIn, s.ComputeMinutes, wantIn, wantMinutes)
		}
	}
}

// What every workspace of a verify run was refused, the baseline's, the
// QA agent's and each inversion's, is the run's, as forja.status reads it.
func TestVerify_egressOfEveryWorkspace_inStatus(t *testing.T) {
	t.Parallel()
	denial := func(destination string) domain.EgressReport {
		return domain.EgressReport{Count: 1, Denied: []domain.EgressDenial{{Kind: domain.EgressHTTP, Destination: destination}}}
	}
	destinations := []string{"baseline.example:80", "qa.example:80", "inversion-1.example:80", "inversion-2.example:80"}
	fx := newVerifyFixture(t, verifyWiring{pr: coveredPull(), fakes: func(fx *verifyFixture) {
		invert(fx, bitAt("test"), bitAt("test"))
		fx.gates.report.Egress = denial(destinations[0])
		fx.qa.result.Egress = denial(destinations[1])
		for k := 1; k <= 2; k++ {
			r := fx.gates.byTree[candidateTree(k)]
			r.Egress = denial(destinations[k+1])
			fx.gates.byTree[candidateTree(k)] = r
		}
	}})

	decodeVerify(t, callVerify(t, fx.cs, verifyPR))
	wire := statusWire(t, fx.cs, "run-1")
	if string(wire["egress_denied_count"]) != "4" {
		t.Errorf("egress_denied_count = %s, want one from each of the 4 workspaces", wire["egress_denied_count"])
	}
	for _, d := range destinations {
		if !strings.Contains(string(wire["egress_denied"]), d) {
			t.Errorf("egress_denied = %s, lacks %s", wire["egress_denied"], d)
		}
	}
}

// What the registry proxy served and refused to every gates run of a
// verify run, the baseline's and each inversion's, is the run's, as
// forja.status reads it.
func TestVerify_registryOfEveryGatesRun_inStatus(t *testing.T) {
	t.Parallel()
	fx := newVerifyFixture(t, verifyWiring{pr: coveredPull(), fakes: func(fx *verifyFixture) {
		invert(fx, bitAt("test"), bitAt("test"))
		fx.gates.report.Registry = registryAccess()
		for k := 1; k <= 2; k++ {
			r := fx.gates.byTree[candidateTree(k)]
			r.Registry = registryAccess()
			fx.gates.byTree[candidateTree(k)] = r
		}
	}})

	decodeVerify(t, callVerify(t, fx.cs, verifyPR))
	if got := string(statusWire(t, fx.cs, "run-1")["registry_access_count"]); got != "12" {
		t.Fatalf("registry_access_count = %s, want the baseline's 4 and each inversion's 4", got)
	}
}

// A candidate whose bundle imports another commit than the one admitted is
// not the inversion that was judged: the run fails at its inversion stage
// before its gates, with nothing published.
func TestVerify_readoptedCandidateNotAdmittedCommit_failsBeforeGates(t *testing.T) {
	t.Parallel()
	moved := fmt.Sprintf("%040x", 0xbad)
	fx := newVerifyFixture(t, verifyWiring{pr: coveredPull(), fakes: func(fx *verifyFixture) {
		invert(fx, bitAt("test"), bitAt("test"))
		fx.cloner.co.readopted = map[string]string{candidateBranch(1): moved}
	}})

	res := callVerify(t, fx.cs, verifyPR)
	if !res.IsError || !strings.Contains(textOf(res), moved) || !strings.Contains(textOf(res), candidateHead(1)) {
		t.Fatalf("IsError %v text %q, want a tool error naming both commits", res.IsError, textOf(res))
	}
	assertNoStatus(t, fx)
	if fx.gates.calls != 1 {
		t.Fatalf("gates ran %d times, want the baseline only", fx.gates.calls)
	}
	assertVerifyFailedAt(t, fx, "inversion")
}

// An inversion of a red head proves nothing: the run fails at the
// baseline, the QA agent never runs and nothing is published.
func TestVerify_headRed_gateFailedNoStatus(t *testing.T) {
	t.Parallel()
	fx := newVerifyFixture(t, verifyWiring{pr: coveredPull(), fakes: func(fx *verifyFixture) {
		invert(fx, bitAt("test"))
		fx.gates.report = redReport()
	}})

	out := decodeVerify(t, callVerify(t, fx.cs, verifyPR))
	assertNoStatus(t, fx)
	if out.State != "failed" || out.FailureReason != "gate_failed" || len(fx.qa.taskList()) != 0 {
		t.Fatalf("state %q reason %q, QA ran %d times; want failed gate_failed before any QA", out.State, out.FailureReason, len(fx.qa.taskList()))
	}
	if !strings.Contains(out.Evidence, "| Baseline gates on the head | failed at `build` |") {
		t.Fatalf("evidence does not name the red baseline:\n%s", out.Evidence)
	}
}

// A head that moved after the pull request was read is not the head the
// status would land on: the clone stage fails before any gate or agent.
func TestVerify_cloneTipNotPRHead_failsBeforeAgent(t *testing.T) {
	t.Parallel()
	co := verifyCheckout(verifyContract)
	co.start = "7777777777777777777777777777777777777777"
	fx := newVerifyFixture(t, verifyWiring{pr: coveredPull(), checkout: co})

	res := callVerify(t, fx.cs, verifyPR)
	if !res.IsError || !strings.Contains(textOf(res), "moved") || !strings.Contains(textOf(res), co.start) {
		t.Fatalf("IsError %v text %q, want a tool error naming the moved head", res.IsError, textOf(res))
	}
	assertNoStatus(t, fx)
	if fx.gates.calls != 0 || len(fx.qa.taskList()) != 0 {
		t.Fatalf("gates %d, QA %d after a moved head, want none", fx.gates.calls, len(fx.qa.taskList()))
	}
	assertVerifyFailedAt(t, fx, "clone")
	if released := fx.claims.releasedClaims(); len(released) != 1 || co.closed != 1 {
		t.Fatalf("released %d claims, closed %d; want 1 and 1", len(released), co.closed)
	}
}

// The wall clock ends the run as a result with its own reason wherever it
// fires, even when the gates it stopped would read as inconclusive; the
// issue is freed and nothing is published.
func TestVerify_wallClockSpent_reasonWallClockReleased(t *testing.T) {
	t.Parallel()
	short := strings.Replace(verifyContract, "wall_clock: 20m", "wall_clock: 50ms", 1)
	tests := map[string]func(*verifyFixture){
		"during the QA agent": func(fx *verifyFixture) {
			fx.qa.run = func(ctx context.Context) (agent.QAResult, error) {
				<-ctx.Done()
				return agent.QAResult{ExitCode: domain.NoExitCode, TimedOut: true, Tokens: spent}, nil
			}
		},
		"during an inversion's gates": func(fx *verifyFixture) {
			invert(fx, bitAt("test"), bitAt("test"))
			fx.gates.untilDeadline = candidateTree(1)
		},
		"during the baseline": func(fx *verifyFixture) { fx.gates.untilDeadline = "/tmp/forja-verify" },
	}
	for name, setUp := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fx := newVerifyFixture(t, verifyWiring{pr: coveredPull(), checkout: verifyCheckout(short), fakes: setUp})

			out := decodeVerify(t, callVerify(t, fx.cs, verifyPR))
			if out.State != "failed" || out.FailureReason != "wall_clock" {
				t.Fatalf("state %q reason %q, want failed wall_clock", out.State, out.FailureReason)
			}
			assertNoStatus(t, fx)
			if released := fx.claims.releasedClaims(); len(released) != 1 || fx.cloner.co.closed != 1 {
				t.Fatalf("released %d claims, closed %d; want 1 and 1", len(released), fx.cloner.co.closed)
			}
		})
	}
}

// The QA agent that the cap stopped, that exited non-zero or that left
// nothing fails the run with nothing published; the cap names itself.
func TestVerify_qaOutcomes_failedNoStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		result agent.QAResult
		reason string
	}{
		{name: "capped", result: agent.QAResult{ExitCode: domain.NoExitCode, Capped: true, Tokens: spent}, reason: "token_cap"},
		{name: "capped after exporting", result: agent.QAResult{ExitCode: 0, Capped: true, Exported: []bool{true, true}, Tokens: spent}, reason: "token_cap"},
		{name: "exit 1", result: agent.QAResult{ExitCode: 1, Exported: []bool{true}, Tokens: spent}},
		{name: "nothing exported", result: agent.QAResult{ExitCode: 0, Tokens: spent}, reason: "inversion_unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fx := newVerifyFixture(t, verifyWiring{pr: coveredPull(), fakes: func(fx *verifyFixture) {
				invert(fx, bitAt("test"), bitAt("test"))
				fx.qa.result = tt.result
			}})

			out := decodeVerify(t, callVerify(t, fx.cs, verifyPR))
			if out.State != "failed" || out.FailureReason != tt.reason || out.TokensIn != 1230 {
				t.Fatalf("state %q reason %q tokens_in %d, want failed %q with the agent's tokens", out.State, out.FailureReason, out.TokensIn, tt.reason)
			}
			assertNoStatus(t, fx)
			if fx.gates.calls != 1 {
				t.Fatalf("gates ran %d times, want the baseline only", fx.gates.calls)
			}
		})
	}
}

// The QA agent works on the head's tree with the base contract, the issue
// as GitHub has it, the pull request's body, the production paths and
// four candidate branches, each with its own bundle outside the tree,
// under the contract's wall clock; the bundles are gone after the run.
func TestVerify_qaTask_baseContractHeadTreeAndCandidates(t *testing.T) {
	t.Parallel()
	fx := newVerifyFixture(t, verifyWiring{pr: coveredPull()})

	decodeVerify(t, callVerify(t, fx.cs, verifyPR))
	tasks := fx.qa.taskList()
	if len(tasks) != 1 {
		t.Fatalf("QA ran %d times, want once", len(tasks))
	}
	assertQATask(t, tasks[0])
	assertCandidates(t, tasks[0].Candidates)
	if left := time.Until(fx.qa.deadline); left > 20*time.Minute || left < 19*time.Minute {
		t.Fatalf("QA deadline in %v, want the base contract's 20m", left)
	}
}

func assertQATask(t *testing.T, task agent.QATask) {
	t.Helper()
	if task.RunID != "run-1" || task.Contract.Image != "golang:1.26" || task.Tree != "/tmp/forja-verify" || task.Issue != verifyStatement {
		t.Fatalf("task = %+v, want run-1 on the head's tree under the base contract with the issue", task)
	}
	if task.PullRequestBody != coveredBody || task.BaseSHA != planSHA || task.HeadSHA != verifyHead {
		t.Fatalf("task body %q base %q head %q", task.PullRequestBody, task.BaseSHA, task.HeadSHA)
	}
	if want := []string{"internal/domain/cost.go", "internal/mcp/cost.go"}; !reflect.DeepEqual(task.Production, want) {
		t.Fatalf("production = %q, want %q", task.Production, want)
	}
}

func assertCandidates(t *testing.T, cands []agent.Candidate) {
	t.Helper()
	if len(cands) != 4 {
		t.Fatalf("candidates = %+v, want 4", cands)
	}
	dir := filepath.Dir(cands[0].Bundle)
	for i, c := range cands {
		if c.Branch != candidateBranch(i+1) || filepath.Dir(c.Bundle) != dir || strings.HasPrefix(c.Bundle, "/tmp/forja-verify") {
			t.Fatalf("candidate %d = %+v, want %s with a bundle beside the others, outside the tree", i, c, candidateBranch(i+1))
		}
	}
	if cands[0].Bundle == cands[1].Bundle {
		t.Fatalf("two candidates share the bundle %s", cands[0].Bundle)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bundle directory %s left after the run: %v", dir, err)
	}
}

// A pull request that is not verifiable never reaches the gates, the
// issue or the QA agent, and its wire says [] for the inversions.
func TestVerify_markdownOnly_noAgentNoGatesInversionsEmpty(t *testing.T) {
	t.Parallel()
	fx := newVerifyFixture(t, verifyWiring{pr: markdownPull()})

	res := callVerify(t, fx.cs, verifyPR)
	decodeVerify(t, res)
	if raw := wireOf(t, res)["inversions"]; string(raw) != "[]" {
		t.Fatalf("inversions = %s, want []", raw)
	}
	if fx.gates.calls != 0 || len(fx.qa.taskList()) != 0 || fx.issues.number != 0 {
		t.Fatalf("gates %d, QA %d, issue %d read for a markdown pull request; want none", fx.gates.calls, len(fx.qa.taskList()), fx.issues.number)
	}
}

// Without a QA agent forja.verify refuses before reserving anything and
// says what to configure.
func TestVerify_noQAAgent_notWiredNamingTheVariables(t *testing.T) {
	t.Parallel()
	fx := newVerifyFixture(t, verifyWiring{pr: markdownPull(), deps: func(d *forja.Deps) { d.QA = nil }})

	res := callVerify(t, fx.cs, verifyPR)
	for _, want := range []string{"not wired", "FORJA_MODEL_API_KEY", "FORJA_AGENT_BINARY"} {
		if !res.IsError || !strings.Contains(textOf(res), want) {
			t.Errorf("IsError %v text %q, want it to name %s", res.IsError, textOf(res), want)
		}
	}
	if got := fx.calls.list(); len(got) != 0 {
		t.Fatalf("calls = %q, want none", got)
	}
}
