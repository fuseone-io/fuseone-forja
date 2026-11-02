package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/agent"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/evidence"
	"github.com/fuseone-io/fuseone-forja/internal/github"
	forja "github.com/fuseone-io/fuseone-forja/internal/mcp"
)

const runBranch = "forja/12"

// checkoutContractSHA is the blob of the base checkout's .forja.yaml.
var checkoutContractSHA = gitBlobSHA(checkoutContract)

// greenEvidence is the body a green run of the fixture renders: the base
// checkout's SHAs, the agent's head, the green report and the tokens, at
// the fixture's fixed clock.
func greenEvidence() string {
	return evidence.Render(evidence.Run{
		Issue: implementIssue, RunID: "run-1", BaseBranch: "main",
		BaseSHA: planSHA, ContractSHA: checkoutContractSHA, HeadSHA: agentHead,
		Gates: greenReport(), Tokens: spent,
		Attempts:    []domain.Attempt{{Number: 1, HeadSHA: agentHead, Gates: greenReport(), Tokens: spent}},
		MaxAttempts: domain.DefaultMaxAttempts,
		Limits:      domain.Limits{WallClock: 20 * time.Minute, MaxDiffLines: 400}, DiffLines: agentDiffLines,
	})
}

func TestImplement_gatesGreen_pushPRThenStatusOnHead(t *testing.T) {
	t.Parallel()
	fx := newImplementFixture(t, contractWithWallClock, nil)

	out := decodeImplement(t, callImplement(t, fx.cs, implementIssue))
	wantCalls := []string{"claim", "put running", "clone", "agent", "gates", "push", "pull_request", "status", "release", "put succeeded"}
	if got := fx.calls.list(); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("calls = %q, want %q", got, wantCalls)
	}
	assertDelivered(t, fx)
	assertGreenOutput(t, out)
}

// assertDelivered checks the push, the pull request and the status a
// green run of the fixture makes, each exactly once.
func assertDelivered(t *testing.T, fx *implementFixture) {
	t.Helper()
	wantPush := []github.PushRequest{{Repository: doctorRepo, Dir: "/tmp/forja-plan-implement-adopted", Head: agentHead, Branch: runBranch}}
	if got := fx.pusher.got(); !reflect.DeepEqual(got, wantPush) {
		t.Fatalf("pushes = %+v, want %+v", got, wantPush)
	}
	wantPR := []github.PullRequestRequest{{Repository: doctorRepo, Title: implementStatement.Title, Head: runBranch, Base: "main", Body: greenEvidence()}}
	if got := fx.prs.got(); !reflect.DeepEqual(got, wantPR) {
		t.Fatalf("pull requests = %+v, want %+v", got, wantPR)
	}
	wantStatus := []github.StatusRequest{{
		Repository: doctorRepo, SHA: agentHead, State: github.StateSuccess, Context: "forja/gates",
		Description: "Every gate of the base commit's contract passed on this commit.", TargetURL: prURL,
	}}
	if got := fx.statuses.got(); !reflect.DeepEqual(got, wantStatus) {
		t.Fatalf("statuses = %+v, want %+v", got, wantStatus)
	}
}

func assertGreenOutput(t *testing.T, out forja.ImplementOutput) {
	t.Helper()
	got := out
	got.Setup, got.Gates, got.Attempts = nil, nil, nil
	if got.DiffLines == nil || *got.DiffLines != agentDiffLines {
		t.Fatalf("diff_lines = %v, want the %d the control plane counted", got.DiffLines, agentDiffLines)
	}
	got.DiffLines = nil
	want := forja.ImplementOutput{
		RunID: "run-1", State: "succeeded", BaseSHA: planSHA, ContractSHA: checkoutContractSHA, HeadSHA: agentHead, Branch: runBranch,
		PullRequest: &forja.PullRequestRef{Number: 41, URL: prURL}, Evidence: greenEvidence(), TokensIn: 1230, TokensOut: 400,
		Attempt: 1, MaxAttempts: 3,
		WallClockSeconds: 1200, MaxDiffLines: 400,
		ProofApparatus: []forja.ChangedPath{}, HumanMergePaths: []forja.ChangedPath{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("output = %+v\nwant %+v", got, want)
	}
	if len(out.Setup) != 1 || len(out.Gates) != 2 || out.Gates[1].Stderr != "warn\n" {
		t.Fatalf("setup %+v gates %+v, want the green report", out.Setup, out.Gates)
	}
	assertOneGreenAttempt(t, out.Attempts)
}

func assertOneGreenAttempt(t *testing.T, attempts []forja.AttemptReport) {
	t.Helper()
	if len(attempts) != 1 || attempts[0].Number != 1 || attempts[0].HeadSHA != agentHead || attempts[0].FailedStep != "" || attempts[0].TokensIn != 1230 {
		t.Fatalf("attempts = %+v, want the one green attempt on the head", attempts)
	}
}

// With no base branch asked for, the pull request targets the branch the
// clone resolved, not the empty input.
func TestImplement_baseBranchUnset_pullRequestTargetsResolvedBranch(t *testing.T) {
	t.Parallel()
	fx := newImplementFixture(t, contractWithWallClock, nil)
	fx.cloner.co.branch = "trunk"

	res, err := fx.cs.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      string(domain.ToolImplement),
		Arguments: map[string]any{"repository": doctorRepo, "issue": implementIssue, "base_branch": ""},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	decodeImplement(t, res)
	if fx.cloner.branch != "" {
		t.Fatalf("cloned branch %q, want the default", fx.cloner.branch)
	}
	if prs := fx.prs.got(); len(prs) != 1 || prs[0].Base != "trunk" {
		t.Fatalf("pull requests = %+v, want one into trunk", prs)
	}
}

// What the gates' workspace was refused and asked its registry proxy for
// is the run's, next to what the agent's was refused.
func TestImplement_gatesEgressAndRegistry_inStatus(t *testing.T) {
	t.Parallel()
	fx := newAgentFixture(t, contractWithWallClock, nil, &fakeAgent{result: agent.Result{Exported: true, Egress: deniedEgress()}})
	fx.gates.report.Egress = domain.EgressReport{Count: 2, Denied: []domain.EgressDenial{{Kind: domain.EgressHTTP, Destination: "gates.example:80"}}}
	fx.gates.report.Registry = registryAccess()

	decodeImplement(t, callImplement(t, fx.cs, implementIssue))
	wire := statusOf(t, fx)
	if string(wire["egress_denied_count"]) != "7" || !strings.Contains(string(wire["egress_denied"]), "gates.example:80") {
		t.Fatalf("egress_denied_count %s, egress_denied %s; want the agent's 5 and the gates' 2", wire["egress_denied_count"], wire["egress_denied"])
	}
	if string(wire["registry_access_count"]) != "4" {
		t.Fatalf("registry_access_count = %s, want the gates' 4", wire["registry_access_count"])
	}
}

// What the agent did to .forja.yaml reaches the tree the gates run on and
// nothing else: the gates run the base checkout's contract, and the
// evidence and forja.status cite that contract's blob.
func TestImplement_agentChangedContract_gatesRunBaseContract(t *testing.T) {
	t.Parallel()
	fx := newImplementFixture(t, contractWithWallClock, nil)
	agentContract := "version: 1\nimage: golang:9.99\ngates:\n  - name: build\n    run: 'true'\n"
	fx.cloner.co.adoptedDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(fx.cloner.co.adoptedDir, ".forja.yaml"), []byte(agentContract), 0o644); err != nil {
		t.Fatal(err)
	}

	out := decodeImplement(t, callImplement(t, fx.cs, implementIssue))
	if fx.gates.contract.Image != "golang:1.26" || fx.gates.contract.Limits.WallClock != 20*time.Minute || fx.gates.contract.Gates[0].Run != "make build" {
		t.Fatalf("gates ran contract %+v, want the base checkout's", fx.gates.contract)
	}
	if fx.gates.tree != fx.cloner.co.adoptedDir {
		t.Fatalf("gates ran on %s, want the agent's tree %s", fx.gates.tree, fx.cloner.co.adoptedDir)
	}
	if !strings.Contains(out.Evidence, "`"+checkoutContractSHA+"`") || strings.Contains(out.Evidence, gitBlobSHA(agentContract)) {
		t.Fatalf("evidence does not cite the base contract's blob %s:\n%s", checkoutContractSHA, out.Evidence)
	}
	if wire := statusOf(t, fx); string(wire["contract_sha"]) != `"`+checkoutContractSHA+`"` || string(wire["base_sha"]) != `"`+planSHA+`"` {
		t.Fatalf("status contract_sha %s base_sha %s, want the base's", wire["contract_sha"], wire["base_sha"])
	}
}

// A red gate on the last attempt is the run's result: nothing leaves the
// control plane, the run fails with the gate's raw log, and the issue is
// freed.
func TestImplement_gateRed_noPushNoPRNoStatus_failedWithGateLogs(t *testing.T) {
	t.Parallel()
	fx := newImplementFixture(t, contractWithWallClock, nil)
	fx.cloner.co.files[".forja.yaml"] = checkoutWithMaxAttempts(1)
	fx.gates.report = redReport()

	out := decodeImplement(t, callImplement(t, fx.cs, implementIssue))
	if n := len(fx.pusher.got()) + len(fx.prs.got()) + len(fx.statuses.got()); n != 0 {
		t.Fatalf("a red gate made %d delivery calls, want none", n)
	}
	if out.State != "failed" || out.FailureReason != "gate_failed" || out.FailedStep != "build" || out.PullRequest != nil {
		t.Fatalf("state %q reason %q failed_step %q pr %+v, want failed gate_failed at build with no PR", out.State, out.FailureReason, out.FailedStep, out.PullRequest)
	}
	assertRedGateStatus(t, statusOf(t, fx))
	if released := fx.claims.releasedClaims(); len(released) != 1 {
		t.Fatalf("released %d claims, want 1", len(released))
	}
}

// assertRedGateStatus checks forja.status gives the failed gate's raw
// stream and exit code, and no pull request.
func assertRedGateStatus(t *testing.T, wire map[string]json.RawMessage) {
	t.Helper()
	var gates []forja.StepReport
	if err := json.Unmarshal(wire["gates"], &gates); err != nil {
		t.Fatalf("gates = %s: %v", wire["gates"], err)
	}
	if len(gates) != 2 || gates[0].ExitCode != 2 || gates[0].Stderr != "undefined: x\n" || gates[1].Status != "skipped" {
		t.Fatalf("status gates = %+v, want build exit 2 with its stderr, test skipped", gates)
	}
	if string(wire["failure_reason"]) != `"gate_failed"` || string(wire["state"]) != `"failed"` {
		t.Fatalf("status state %s reason %s", wire["state"], wire["failure_reason"])
	}
	if _, present := wire["pull_request"]; present {
		t.Fatalf("pull_request on the wire of a run that opened none: %s", wire["pull_request"])
	}
}

// An agent that exited 0 without a commit beyond the base fails the run
// before any gate, whether the workspace said so or the import found it.
func TestImplement_noCommits_failedNoGates(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*implementFixture){
		"nothing exported":      func(fx *implementFixture) { fx.agent.result = agent.Result{ExitCode: 0, Tokens: spent} },
		"exported head is base": func(fx *implementFixture) { fx.cloner.co.head = planSHA },
	}
	for name, setUp := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fx := newImplementFixture(t, contractWithWallClock, nil)
			setUp(fx)

			out := decodeImplement(t, callImplement(t, fx.cs, implementIssue))
			if fx.gates.calls != 0 || len(fx.pusher.got()) != 0 {
				t.Fatalf("gates ran %d times, pushes %d, want none", fx.gates.calls, len(fx.pusher.got()))
			}
			if out.State != "failed" || out.FailureReason != "no_commits" || out.HeadSHA != "" {
				t.Fatalf("state %q reason %q head %q, want failed no_commits with no head", out.State, out.FailureReason, out.HeadSHA)
			}
			if released := fx.claims.releasedClaims(); len(released) != 1 {
				t.Fatalf("released %d claims, want 1", len(released))
			}
		})
	}
}

func TestImplement_agentNonZero_noExportNoGates(t *testing.T) {
	t.Parallel()
	fx := newAgentFixture(t, contractWithWallClock, nil, &fakeAgent{result: agent.Result{ExitCode: 1, Tokens: spent}})

	out := decodeImplement(t, callImplement(t, fx.cs, implementIssue))
	if len(fx.cloner.co.adopted) != 0 || fx.gates.calls != 0 {
		t.Fatalf("adopted %d bundles, gates ran %d times, want neither", len(fx.cloner.co.adopted), fx.gates.calls)
	}
	if out.State != "failed" || out.FailureReason != "" {
		t.Fatalf("state %q reason %q, want failed with no reason", out.State, out.FailureReason)
	}
}

// Whichever stage fails, the run is recorded failed with that stage
// failed, the issue is freed once, the checkout is discarded and the
// bundle the agent exported is gone.
func TestImplement_eachStageFails_claimReleasedRunFailedCloneClosed(t *testing.T) {
	t.Parallel()
	down := errors.New("stage down")
	tests := []struct {
		name    string
		stage   string
		breakIt func(*implementFixture)
	}{
		{name: "clone", stage: "clone", breakIt: func(fx *implementFixture) { fx.cloner.err = down }},
		{name: "agent", stage: "agent", breakIt: func(fx *implementFixture) { fx.agent.err = down }},
		{name: "adopt", stage: "agent", breakIt: func(fx *implementFixture) { fx.cloner.co.adoptErr = down }},
		{name: "gates error", stage: "gates", breakIt: func(fx *implementFixture) { fx.gates.err = down }},
		{name: "push", stage: "push", breakIt: func(fx *implementFixture) { fx.pusher.err = down }},
		{name: "pull request", stage: "pull_request", breakIt: func(fx *implementFixture) { fx.prs.err = down }},
		{name: "status", stage: "status", breakIt: func(fx *implementFixture) { fx.statuses.err = down }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fx := newImplementFixture(t, contractWithWallClock, nil)
			tt.breakIt(fx)

			res := callImplement(t, fx.cs, implementIssue)
			if !res.IsError || !strings.Contains(textOf(res), "stage down") {
				t.Fatalf("IsError %v text %q, want the stage's failure as a tool error", res.IsError, textOf(res))
			}
			if released := fx.claims.releasedClaims(); len(released) != 1 {
				t.Fatalf("released %d claims, want 1", len(released))
			}
			assertFailedAt(t, fx, tt.stage)
			if tt.stage != "clone" && fx.cloner.co.closed != 1 {
				t.Fatalf("checkout closed %d times, want once", fx.cloner.co.closed)
			}
			assertBundleGone(t, fx)
		})
	}
}

// assertFailedAt checks the stored run is failed and finished, with stage
// failed and the release after it.
func assertFailedAt(t *testing.T, fx *implementFixture, stage string) {
	t.Helper()
	run, err := fx.runs.Get(context.Background(), "run-1")
	if err != nil || run.State != domain.StateFailed || run.FinishedAt == nil {
		t.Fatalf("run = %+v, %v; want failed and finished", run, err)
	}
	n := len(run.Stages)
	if n < 2 || string(run.Stages[n-2].Name) != stage || run.Stages[n-2].Outcome != domain.StageFailed || run.Stages[n-1].Name != domain.StageRelease {
		t.Fatalf("stages = %+v, want %s failed then the release", run.Stages, stage)
	}
}

func assertBundleGone(t *testing.T, fx *implementFixture) {
	t.Helper()
	fx.agent.mu.Lock()
	defer fx.agent.mu.Unlock()
	for _, task := range fx.agent.tasks {
		if _, err := os.Stat(filepath.Dir(task.Bundle)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("bundle directory %s left behind: %v", filepath.Dir(task.Bundle), err)
		}
	}
}

// One deadline covers the run from the agent to the status: the gates and
// every delivery call run under the agent's own end, the contract's wall
// clock counted from the claim.
func TestImplement_gatesAndDelivery_underRunDeadline(t *testing.T) {
	t.Parallel()
	fx := newImplementFixture(t, contractWithWallClock, nil)

	decodeImplement(t, callImplement(t, fx.cs, implementIssue))
	end := fx.agent.deadline
	if end.IsZero() || !fx.gates.hasDL {
		t.Fatalf("agent deadline %v, gates have one %v; want both", end, fx.gates.hasDL)
	}
	for what, dl := range map[string]time.Time{
		"gates": fx.gates.deadline, "push": fx.pusher.deadline(), "pull_request": fx.prs.deadline(), "status": fx.statuses.deadline(),
	} {
		if !dl.Equal(end) {
			t.Errorf("%s deadline %v, want the agent's %v", what, dl, end)
		}
	}
}
