package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/agent"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/evidence"
	"github.com/fuseone-io/fuseone-forja/internal/github"
	forja "github.com/fuseone-io/fuseone-forja/internal/mcp"
)

const (
	// forjaLogin is the login Forja's credential acts as.
	forjaLogin = "forja-dev[bot]"
	// branchTip is the head an earlier run pushed to forja/12; roundHead the
	// commit the round's agent leaves on top of it.
	branchTip = "5f814f9aa0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5"
	roundHead = "8b1cac1aa0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5"
	// roundFinding stands for a reviewer's text; no test prints it.
	roundFinding = "Rename the meter to usage; reviewer-private-note-77.\n"
	foundURL     = "https://github.com/" + doctorRepo + "/pull/57"
)

// foundPR is the open pull request of forja/12, opened by Forja into main.
var foundPR = domain.PullRequest{Number: 57, URL: foundURL, Author: forjaLogin, Base: "main", HeadRepository: doctorRepo}

// branchCheckout is the base checkout with forja/12 checked out at its tip:
// reads answer for the base, Start is the tip.
func branchCheckout() *fakeCheckout {
	co := implementCheckout()
	co.start, co.head = branchTip, roundHead
	co.dir, co.adoptedDir = "/tmp/forja-plan-round", "/tmp/forja-plan-round-adopted"
	return co
}

// fakeBranchCloner hands out one checkout with the branch already checked
// out, as the real CloneBranch leaves it, or fails before any exists.
type fakeBranchCloner struct {
	co  *fakeCheckout
	err error
	log *callLog

	calls              int
	repo, base, branch string
}

func (f *fakeBranchCloner) CloneBranch(_ context.Context, repo, base, branch string) (forja.Checkout, error) {
	if f.log != nil {
		f.log.add("clone")
	}
	f.calls++
	f.repo, f.base, f.branch = repo, base, branch
	if f.err != nil {
		return nil, f.err
	}
	f.co.started = branch
	return f.co, nil
}

// fakeIdentity answers login, or fails with err.
type fakeIdentity struct {
	login string
	err   error
	calls *callLog

	mu sync.Mutex
	n  int
}

func (f *fakeIdentity) Login(context.Context) (string, error) {
	if f.calls != nil {
		f.calls.add("identity")
	}
	f.mu.Lock()
	f.n++
	f.mu.Unlock()
	return f.login, f.err
}

func callRound(t *testing.T, cs *sdk.ClientSession, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	full := map[string]any{"repository": doctorRepo, "issue": implementIssue, "base_branch": "", "branch": runBranch, "finding": roundFinding}
	for k, v := range args {
		if v == nil {
			delete(full, k)
			continue
		}
		full[k] = v
	}
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: string(domain.ToolImplement), Arguments: full})
	if err != nil {
		t.Fatalf("CallTool(forja.implement): protocol error %v", err)
	}
	return res
}

// roundEvidence is the comment a green round of the fixture posts.
func roundEvidence() string {
	return evidence.Render(evidence.Run{
		Issue: implementIssue, RunID: "run-1", BaseBranch: "main",
		BaseSHA: planSHA, ContractSHA: checkoutContractSHA, StartSHA: branchTip, HeadSHA: roundHead,
		Gates: greenReport(), Tokens: spent,
	})
}

func TestImplement_correctionRound_gatesGreen_pushCommentThenStatusOnNewHead(t *testing.T) {
	t.Parallel()
	fx := newImplementFixture(t, contractWithWallClock, nil)

	out := decodeImplement(t, callRound(t, fx.cs, nil))
	want := []string{"find", "identity", "claim", "put running", "clone", "agent", "gates", "push", "comment", "status", "release", "put succeeded"}
	if got := fx.calls.list(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	if fx.cloner.calls != 0 || fx.branchCloner.calls != 1 || fx.branchCloner.base != "main" || fx.branchCloner.branch != runBranch {
		t.Fatalf("Clone %d times, CloneBranch %d times of %s on %q; want only CloneBranch of forja/12 on the PR's base", fx.cloner.calls, fx.branchCloner.calls, fx.branchCloner.branch, fx.branchCloner.base)
	}
	if n := len(fx.prs.got()); n != 0 {
		t.Fatalf("a round opened %d pull requests, want none", n)
	}
	assertRoundDelivered(t, fx)
	assertRoundOutput(t, out)
	assertRoundStages(t, statusOf(t, fx))
	assertFindingNowhere(t, fx, out)
}

// assertRoundDelivered checks the push of the new head to the same branch,
// the comment with the evidence on the found pull request, and forja/gates
// on the new head pointing at it.
func assertRoundDelivered(t *testing.T, fx *implementFixture) {
	t.Helper()
	wantPush := []github.PushRequest{{Repository: doctorRepo, Dir: "/tmp/forja-plan-round-adopted", Head: roundHead, Branch: runBranch}}
	if got := fx.pusher.got(); !reflect.DeepEqual(got, wantPush) {
		t.Fatalf("pushes = %+v, want %+v", got, wantPush)
	}
	comments := fx.prs.comments.got()
	if len(comments) != 1 || comments[0].Repository != doctorRepo || comments[0].Number != 57 || comments[0].Body != roundEvidence() {
		t.Fatalf("comments = %d, want one on #57 with the round's evidence", len(comments))
	}
	if !strings.Contains(comments[0].Body, "`"+roundHead+"`") || !strings.Contains(comments[0].Body, "`"+branchTip+"`") {
		t.Fatal("the comment does not name the new head and the tip the round started from")
	}
	wantStatus := []github.StatusRequest{{
		Repository: doctorRepo, SHA: roundHead, State: github.StateSuccess, Context: "forja/gates",
		Description: "Every gate of the base commit's contract passed on this commit.", TargetURL: foundURL,
	}}
	if got := fx.statuses.got(); !reflect.DeepEqual(got, wantStatus) {
		t.Fatalf("statuses = %+v, want %+v", got, wantStatus)
	}
}

func assertRoundOutput(t *testing.T, out forja.ImplementOutput) {
	t.Helper()
	if out.State != "succeeded" || out.HeadSHA != roundHead || out.BaseSHA != planSHA || out.Evidence != roundEvidence() {
		t.Fatalf("output state %q head %q base %q, want succeeded on the new head over the base with the round's evidence", out.State, out.HeadSHA, out.BaseSHA)
	}
	if out.PullRequest == nil || *out.PullRequest != (forja.PullRequestRef{Number: 57, URL: foundURL}) {
		t.Fatalf("output pull request %+v, want the found #57", out.PullRequest)
	}
}

func assertRoundStages(t *testing.T, wire map[string]json.RawMessage) {
	t.Helper()
	var stages []forja.StageReport
	if err := json.Unmarshal(wire["stages"], &stages); err != nil {
		t.Fatalf("stages = %s: %v", wire["stages"], err)
	}
	var names []string
	for _, s := range stages {
		names = append(names, s.Name)
	}
	if want := []string{"claim", "clone", "agent", "gates", "push", "comment", "status", "release"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("stages = %q, want %q", names, want)
	}
}

// The finding reaches the agent's task and nothing else: not the comment,
// not forja.status, not the tool's answer.
func assertFindingNowhere(t *testing.T, fx *implementFixture, out forja.ImplementOutput) {
	t.Helper()
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	status, err := json.Marshal(statusOf(t, fx))
	if err != nil {
		t.Fatal(err)
	}
	for what, text := range map[string]string{"output": string(raw), "status": string(status), "comment": fx.prs.comments.got()[0].Body} {
		if strings.Contains(text, "reviewer-private-note-77") {
			t.Errorf("the finding reached the %s", what)
		}
	}
}

// The agent starts from the branch tip with the finding; the contract and
// the context are still the base commit's.
func TestImplement_correctionRound_agentTaskStartsFromBranchTipWithFinding(t *testing.T) {
	t.Parallel()
	fx := newImplementFixture(t, contractWithWallClock, nil)

	decodeImplement(t, callRound(t, fx.cs, nil))
	task := fx.agent.task(t)
	if task.BaseSHA != branchTip || task.Branch != runBranch || task.Tree != "/tmp/forja-plan-round" {
		t.Fatalf("task base %q branch %q tree %q, want forja/12 at the tip %s on the branch's tree", task.BaseSHA, task.Branch, task.Tree, branchTip)
	}
	if task.Finding != roundFinding {
		t.Fatalf("task finding has %d bytes, want the round's %d", len(task.Finding), len(roundFinding))
	}
	if task.Contract.Image != "golang:1.26" || task.Issue != implementStatement {
		t.Fatalf("task image %q issue %+v, want the base checkout's contract and the issue", task.Contract.Image, task.Issue)
	}
	want := []agent.File{{Path: "CLAUDE.md", Content: "# Rules\n"}, {Path: "docs/a.md", Content: "a\n"}, {Path: "docs/b.md", Content: "b\n"}}
	if !reflect.DeepEqual(task.Context, want) {
		t.Fatalf("context = %+v, want the base's %+v", task.Context, want)
	}
}

// Rule 2 in a round: earlier agent commits on the branch rewrote
// .forja.yaml, and the gates still run the base commit's contract.
func TestImplement_correctionRound_branchChangedContract_gatesRunBaseContract(t *testing.T) {
	t.Parallel()
	fx := newImplementFixture(t, contractWithWallClock, nil)
	branchContract := "version: 1\nimage: golang:9.99\ngates:\n  - name: build\n    run: 'true'\n"
	tree := t.TempDir()
	if err := os.WriteFile(filepath.Join(tree, ".forja.yaml"), []byte(branchContract), 0o644); err != nil {
		t.Fatal(err)
	}
	fx.branchCloner.co.dir, fx.branchCloner.co.adoptedDir = tree, tree

	out := decodeImplement(t, callRound(t, fx.cs, nil))
	if fx.gates.contract.Image != "golang:1.26" || fx.gates.contract.Gates[0].Run != "make build" || fx.gates.tree != tree {
		t.Fatalf("gates ran %+v on %s, want the base checkout's contract on the branch's tree", fx.gates.contract, fx.gates.tree)
	}
	if fx.agent.task(t).Contract.Image != "golang:1.26" {
		t.Fatal("the agent was handed the branch's contract")
	}
	if !strings.Contains(out.Evidence, "`"+checkoutContractSHA+"`") || strings.Contains(out.Evidence, gitBlobSHA(branchContract)) {
		t.Fatalf("evidence does not cite the base contract's blob %s", checkoutContractSHA)
	}
	if wire := statusOf(t, fx); string(wire["contract_sha"]) != `"`+checkoutContractSHA+`"` {
		t.Fatalf("status contract_sha %s, want the base's", wire["contract_sha"])
	}
}

func TestImplement_correctionRound_gateRed_noPushNoCommentNoStatus(t *testing.T) {
	t.Parallel()
	fx := newImplementFixture(t, contractWithWallClock, nil)
	fx.gates.report = redReport()

	out := decodeImplement(t, callRound(t, fx.cs, nil))
	if n := len(fx.pusher.got()) + len(fx.prs.got()) + len(fx.prs.comments.got()) + len(fx.statuses.got()); n != 0 {
		t.Fatalf("a red gate made %d delivery calls, want none", n)
	}
	if out.State != "failed" || out.FailureReason != "gate_failed" || out.FailedStep != "build" {
		t.Fatalf("state %q reason %q failed_step %q, want failed gate_failed at build", out.State, out.FailureReason, out.FailedStep)
	}
	if released := fx.claims.releasedClaims(); len(released) != 1 {
		t.Fatalf("released %d claims, want 1", len(released))
	}
}

// Every way a branch can fail to be one Forja opened is refused before
// the claim: nothing is reserved and no run is recorded.
func TestImplement_correctionRound_notForjaBranch_refusedNoClaimNoRun(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		args     map[string]any
		breakIt  func(*implementFixture)
		wantFind bool
	}{
		{name: "not a forja branch", args: map[string]any{"branch": "feature/12"}},
		{name: "another issue's branch", args: map[string]any{"branch": "forja/13"}},
		{name: "the issue's branch with a suffix", args: map[string]any{"branch": "forja/12/x"}},
		{name: "no open pull request", breakIt: func(fx *implementFixture) { fx.prs.found = domain.PullRequest{} }, wantFind: true},
		{name: "more than one open pull request", breakIt: func(fx *implementFixture) {
			fx.prs.finds.err = fmt.Errorf("fake: %w", github.ErrManyPullRequests)
		}, wantFind: true},
		{name: "author is not Forja", breakIt: func(fx *implementFixture) { fx.prs.found.Author = "phbpx" }, wantFind: true},
		{name: "author is Forja without the bot suffix", breakIt: func(fx *implementFixture) { fx.prs.found.Author = "forja-dev" }, wantFind: true},
		{name: "head in another repository", breakIt: func(fx *implementFixture) { fx.prs.found.HeadRepository = "someone/fuseone-forja" }, wantFind: true},
		{name: "base branch other than the pull request's", args: map[string]any{"base_branch": "release"}, wantFind: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fx := newImplementFixture(t, contractWithWallClock, nil)
			if tt.breakIt != nil {
				tt.breakIt(fx)
			}

			res := callRound(t, fx.cs, tt.args)
			if !res.IsError || !strings.Contains(textOf(res), forja.ErrNotForjaBranch.Error()) {
				t.Fatalf("IsError %v text %q, want a tool error naming %q", res.IsError, textOf(res), forja.ErrNotForjaBranch)
			}
			if strings.Contains(textOf(res), "reviewer-private-note-77") {
				t.Fatal("the refusal carries the finding")
			}
			assertNothingReserved(t, fx)
			if found := len(fx.prs.finds.got()) > 0; found != tt.wantFind {
				t.Fatalf("Find called %v, want %v: a branch name is refused before any I/O", found, tt.wantFind)
			}
		})
	}
}

// assertNothingReserved checks no claim was asked for, no run recorded and
// no agent run.
func assertNothingReserved(t *testing.T, fx *implementFixture) {
	t.Helper()
	if reqs := fx.claims.requested(); len(reqs) != 0 {
		t.Fatalf("claims requested = %+v, want none", reqs)
	}
	if _, err := fx.runs.Get(context.Background(), "run-1"); !errors.Is(err, domain.ErrRunNotFound) {
		t.Fatalf("a refused round left a run in the store: %v", err)
	}
	if fx.agent.tasks != nil || fx.branchCloner.calls != 0 {
		t.Fatal("a refused round cloned or ran the agent")
	}
}

// A lookup that fails on its own terms, not a refusal, is still a tool
// error before the claim.
func TestImplement_correctionRound_findOrLoginFails_toolErrorNoClaim(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*implementFixture){
		"find":  func(fx *implementFixture) { fx.prs.finds.err = errors.New("lookup down") },
		"login": func(fx *implementFixture) { fx.identity.err = errors.New("lookup down") },
	}
	for name, breakIt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fx := newImplementFixture(t, contractWithWallClock, nil)
			breakIt(fx)

			res := callRound(t, fx.cs, nil)
			if !res.IsError || !strings.Contains(textOf(res), "lookup down") {
				t.Fatalf("IsError %v text %q, want the lookup's failure as a tool error", res.IsError, textOf(res))
			}
			assertNothingReserved(t, fx)
		})
	}
}

// A branch and a finding come together, and a finding over 64 KiB is
// refused, all before any call.
func TestImplement_correctionRound_branchWithoutFinding_refusedBeforeAnyCall(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "branch without finding", args: map[string]any{"finding": nil}, want: "needs both branch and finding"},
		{name: "finding without branch", args: map[string]any{"branch": nil}, want: "needs both branch and finding"},
		{name: "finding over 64 KiB", args: map[string]any{"finding": strings.Repeat("f", 64<<10+1)}, want: "finding of 65537 bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fx := newImplementFixture(t, contractWithWallClock, nil)

			res := callRound(t, fx.cs, tt.args)
			if !res.IsError || !strings.Contains(textOf(res), tt.want) {
				t.Fatalf("IsError %v, %d bytes of text; want a tool error saying %q", res.IsError, len(textOf(res)), tt.want)
			}
			if got := fx.calls.list(); len(got) != 0 {
				t.Fatalf("calls = %q, want none", got)
			}
		})
	}
}

// A base branch named by the caller is accepted when it is the pull
// request's, and the round works on that base.
func TestImplement_correctionRound_baseBranchOfThePullRequest_accepted(t *testing.T) {
	t.Parallel()
	fx := newImplementFixture(t, contractWithWallClock, nil)
	fx.prs.found.Base = "release"

	out := decodeImplement(t, callRound(t, fx.cs, map[string]any{"base_branch": "release"}))
	if out.State != "succeeded" || fx.branchCloner.base != "release" {
		t.Fatalf("state %q, cloned on %q; want a succeeded round on release", out.State, fx.branchCloner.base)
	}
}

// 64 KiB is the largest finding a round takes.
func TestImplement_correctionRound_findingOf64KiB_accepted(t *testing.T) {
	t.Parallel()
	fx := newImplementFixture(t, contractWithWallClock, nil)

	out := decodeImplement(t, callRound(t, fx.cs, map[string]any{"finding": strings.Repeat("f", 64<<10)}))
	if out.State != "succeeded" || len(fx.agent.task(t).Finding) != 64<<10 {
		t.Fatalf("state %q, finding of %d bytes, want a succeeded round with the whole finding", out.State, len(fx.agent.task(t).Finding))
	}
}

// A round reserves the issue exactly as a first run does and frees it
// whether it delivered or failed.
func TestImplement_correctionRound_claimsIssueAndReleasesIt(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*implementFixture){
		"delivered":     func(*implementFixture) {},
		"push fails":    func(fx *implementFixture) { fx.pusher.err = errors.New("stage down") },
		"comment fails": func(fx *implementFixture) { fx.prs.comments.err = errors.New("stage down") },
	}
	for name, breakIt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fx := newImplementFixture(t, contractWithWallClock, nil)
			breakIt(fx)

			callRound(t, fx.cs, nil)
			want := domain.ClaimRequest{Repository: doctorRepo, Issue: implementIssue, RunID: "run-1", Lease: 45 * time.Minute}
			if reqs := fx.claims.requested(); len(reqs) != 1 || reqs[0] != want {
				t.Fatalf("claims requested = %+v, want [%+v]", reqs, want)
			}
			if released := fx.claims.releasedClaims(); len(released) != 1 || released[0].RunID != "run-1" {
				t.Fatalf("released = %+v, want the round's claim", released)
			}
		})
	}
}

func TestImplement_correctionRound_issueHeld_toolErrorNamesHolderNoRunNoAgent(t *testing.T) {
	t.Parallel()
	holder := domain.Claim{RunID: "run-holder", Repository: doctorRepo, Issue: implementIssue, ExpiresAt: startedAt().Add(45 * time.Minute)}
	fx := newImplementFixture(t, contractWithWallClock, func(f *fakeClaims) {
		f.claimErr = &domain.ClaimHeldError{Holder: holder}
	})

	res := callRound(t, fx.cs, nil)
	if !res.IsError || !strings.Contains(textOf(res), "run-holder") {
		t.Fatalf("IsError %v text %q, want a tool error naming the holder", res.IsError, textOf(res))
	}
	if got, want := fx.calls.list(), []string{"find", "identity", "claim"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	if _, err := fx.runs.Get(context.Background(), "run-1"); !errors.Is(err, domain.ErrRunNotFound) {
		t.Fatalf("a refused claim left a run in the store: %v", err)
	}
	if fx.agent.tasks != nil {
		t.Fatal("the agent ran on an issue another run holds")
	}
}

// Without a branch cloner or an identity the tool refuses before reserving
// anything, naming each.
func TestImplement_noBranchClonerOrIdentity_notWiredNamingThem(t *testing.T) {
	t.Parallel()
	fx := newWiredFixture(t, contractWithWallClock, fixtureWiring{deps: func(d *forja.Deps) {
		d.BranchCloner, d.Identity = nil, nil
	}})

	res := callImplement(t, fx.cs, implementIssue)
	for _, want := range []string{"not wired", "branch cloner", "identity"} {
		if !res.IsError || !strings.Contains(textOf(res), want) {
			t.Errorf("IsError %v, text %q; want it to name %s", res.IsError, textOf(res), want)
		}
	}
	if got := fx.calls.list(); len(got) != 0 {
		t.Fatalf("calls = %q, want none", got)
	}
}
