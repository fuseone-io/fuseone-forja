package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	forja "github.com/fuseone-io/fuseone-forja/internal/mcp"
)

func markdownPull() domain.PullRequestDiff {
	return verifyPull(changed("README.md", "docs/x.md")...)
}

// The pull request is read, the issue its branch names is claimed
// with the lease the contract sizes, the base is cloned, forja/qa is
// published and the claim released, in that order, around the records.
func TestVerify_free_readsClaimsClonesPublishesReleases(t *testing.T) {
	t.Parallel()
	fx := newVerifyFixture(t, verifyWiring{pr: markdownPull()})

	out := decodeVerify(t, callVerify(t, fx.cs, verifyPR))
	want := []string{"read pull request", "claim", "put running", "clone", "status", "release", "put succeeded"}
	if got := fx.calls.list(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	wantClaim := domain.ClaimRequest{Repository: doctorRepo, Issue: implementIssue, RunID: "run-1", Lease: 45 * time.Minute}
	if reqs := fx.claims.requested(); len(reqs) != 1 || reqs[0] != wantClaim {
		t.Fatalf("claims = %+v, want [%+v]", reqs, wantClaim)
	}
	if released := fx.claims.releasedClaims(); len(released) != 1 || released[0].RunID != out.RunID {
		t.Fatalf("released = %+v, want the claim of %s", released, out.RunID)
	}
	if fx.cloner.repo != doctorRepo || fx.cloner.base != "main" || fx.cloner.branch != "forja/12" || fx.cloner.co.closed != 1 {
		t.Fatalf("cloned %s %s on %s, closed %d times; want %s forja/12 on main closed once", fx.cloner.repo, fx.cloner.branch, fx.cloner.base, fx.cloner.co.closed, doctorRepo)
	}
}

// Whatever ends the run, the run is recorded failed with the stage that
// ended it failed, the issue is freed once and the clone discarded.
func TestVerify_eachFailure_claimReleasedRunFailed(t *testing.T) {
	t.Parallel()
	down := errors.New("stage down")
	tests := []struct {
		name      string
		stage     string
		pr        domain.PullRequestDiff
		breakIt   func(*verifyFixture)
		wantError string
	}{
		{name: "clone", stage: "clone", pr: markdownPull(), breakIt: func(fx *verifyFixture) { fx.cloner.err = down }, wantError: "stage down"},
		{name: "contract rejected", stage: "clone", pr: markdownPull(), breakIt: func(fx *verifyFixture) {
			fx.cloner.co.files[".forja.yaml"] = string(invalidContract)
		}, wantError: "gates[0].run"},
		{name: "contract missing", stage: "clone", pr: markdownPull(), breakIt: func(fx *verifyFixture) {
			delete(fx.cloner.co.files, ".forja.yaml")
		}, wantError: "contract missing"},
		{name: "status", stage: "status", pr: markdownPull(), breakIt: func(fx *verifyFixture) { fx.statuses.err = down }, wantError: "stage down"},
		{name: "production diff, no candidate", stage: "agent", pr: verifyPull(changed("internal/verify/classify.go")...), breakIt: func(*verifyFixture) {}},
		{name: "gates", stage: "gates", pr: coveredPull(), breakIt: func(fx *verifyFixture) { fx.gates.err = down }, wantError: "stage down"},
		{name: "head red", stage: "gates", pr: coveredPull(), breakIt: func(fx *verifyFixture) { fx.gates.report = redReport() }},
		{name: "agent", stage: "agent", pr: coveredPull(), breakIt: func(fx *verifyFixture) { fx.qa.err = down }, wantError: "stage down"},
		{name: "issue", stage: "agent", pr: coveredPull(), breakIt: func(fx *verifyFixture) { fx.issues.err = down }, wantError: "stage down"},
		{name: "inversion", stage: "inversion", pr: coveredPull(), breakIt: func(fx *verifyFixture) {
			invert(fx, bitAt("test"), bitAt("test"))
			fx.gates.errByTree = map[string]error{candidateTree(1): down}
		}, wantError: "stage down"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fx := newVerifyFixture(t, verifyWiring{pr: tt.pr})
			tt.breakIt(fx)

			res := callVerify(t, fx.cs, verifyPR)
			if res.IsError != (tt.wantError != "") || !strings.Contains(textOf(res), tt.wantError) {
				t.Fatalf("IsError %v text %q, want a tool error %q only when the infrastructure failed", res.IsError, textOf(res), tt.wantError)
			}
			if released := fx.claims.releasedClaims(); len(released) != 1 {
				t.Fatalf("released %d claims, want 1", len(released))
			}
			assertVerifyFailedAt(t, fx, tt.stage)
			if fx.cloner.err == nil && fx.cloner.co.closed != 1 {
				t.Fatalf("checkout closed %d times, want once", fx.cloner.co.closed)
			}
		})
	}
}

func assertVerifyFailedAt(t *testing.T, fx *verifyFixture, stage string) {
	t.Helper()
	run, err := fx.runs.Get(context.Background(), "run-1")
	if err != nil || run.State != domain.StateFailed || run.FinishedAt == nil || run.Tool != domain.ToolVerify {
		t.Fatalf("run = %+v, %v; want a failed, finished forja.verify run", run, err)
	}
	n := len(run.Stages)
	if n < 2 || string(run.Stages[n-2].Name) != stage || run.Stages[n-2].Outcome != domain.StageFailed || run.Stages[n-1].Name != domain.StageRelease {
		t.Fatalf("stages = %+v, want %s failed then the release", run.Stages, stage)
	}
}

func TestVerify_held_toolErrorNamesHolderAndRecordsNoRun(t *testing.T) {
	t.Parallel()
	holder := domain.Claim{RunID: "run-holder", Repository: doctorRepo, Issue: implementIssue, ExpiresAt: startedAt().Add(45 * time.Minute)}
	fx := newVerifyFixture(t, verifyWiring{pr: markdownPull(), claims: func(f *fakeClaims) {
		f.claimErr = &domain.ClaimHeldError{Holder: holder}
	}})

	res := callVerify(t, fx.cs, verifyPR)
	if !res.IsError || !strings.Contains(textOf(res), "run-holder") || !strings.Contains(textOf(res), string(domain.ToolVerify)) {
		t.Fatalf("IsError %v text %q, want a forja.verify tool error naming the holder", res.IsError, textOf(res))
	}
	if got, want := fx.calls.list(), []string{"read pull request", "claim"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q, want %q: no run recorded, nothing released", got, want)
	}
	if _, err := fx.runs.Get(context.Background(), "run-1"); !errors.Is(err, domain.ErrRunNotFound) {
		t.Fatalf("a refused claim left a run in the store: %v", err)
	}
}

// Only a pull request implement could have opened names an issue to
// claim: its head is forja/<positive int> in the repository itself. Any
// other head, and a pull request that cannot be read, is a tool error
// before any claim.
func TestVerify_headNotARunBranch_toolErrorBeforeAnyClaim(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*domain.PullRequestDiff)
		number int
	}{
		{name: "feature branch", mutate: func(p *domain.PullRequestDiff) { p.HeadRef = "feat/12-x" }},
		{name: "prefix only", mutate: func(p *domain.PullRequestDiff) { p.HeadRef = "forja/" }},
		{name: "issue zero", mutate: func(p *domain.PullRequestDiff) { p.HeadRef = "forja/0" }},
		{name: "negative issue", mutate: func(p *domain.PullRequestDiff) { p.HeadRef = "forja/-12" }},
		{name: "signed issue", mutate: func(p *domain.PullRequestDiff) { p.HeadRef = "forja/+12" }},
		{name: "leading zero", mutate: func(p *domain.PullRequestDiff) { p.HeadRef = "forja/012" }},
		{name: "nested", mutate: func(p *domain.PullRequestDiff) { p.HeadRef = "forja/12/x" }},
		{name: "not a number", mutate: func(p *domain.PullRequestDiff) { p.HeadRef = "forja/twelve" }},
		{name: "fork", mutate: func(p *domain.PullRequestDiff) { p.HeadRepository = "someone/fuseone-forja" }},
		{name: "head repository gone", mutate: func(p *domain.PullRequestDiff) { p.HeadRepository = "" }},
		{name: "no base branch", mutate: func(p *domain.PullRequestDiff) { p.BaseRef = "" }},
		{name: "unknown pull request", mutate: func(*domain.PullRequestDiff) {}, number: 404},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pr := markdownPull()
			tt.mutate(&pr)
			fx := newVerifyFixture(t, verifyWiring{pr: pr})
			number := verifyPR
			if tt.number != 0 {
				number = tt.number
			}

			res := callVerify(t, fx.cs, number)
			if !res.IsError || !strings.Contains(textOf(res), string(domain.ToolVerify)) {
				t.Fatalf("IsError %v text %q, want a forja.verify tool error", res.IsError, textOf(res))
			}
			if reqs := fx.claims.requested(); len(reqs) != 0 {
				t.Fatalf("claimed %+v for a pull request that names no run's issue", reqs)
			}
			if got := fx.statuses.got(); len(got) != 0 {
				t.Fatalf("published %+v", got)
			}
		})
	}
}

// A pull request number that is not positive is refused before GitHub is
// asked anything.
func TestVerify_numberNotPositive_toolErrorBeforeAnyCall(t *testing.T) {
	t.Parallel()
	fx := newVerifyFixture(t, verifyWiring{pr: markdownPull()})

	res := callVerify(t, fx.cs, 0)
	if !res.IsError || !strings.Contains(textOf(res), "pull request") {
		t.Fatalf("IsError %v text %q, want a tool error naming the pull request", res.IsError, textOf(res))
	}
	if got := fx.calls.list(); len(got) != 0 {
		t.Fatalf("calls = %q, want none", got)
	}
}

// The contract applied is the base checkout's, never the one the lease
// was sized from: here the API's excludes every path and the checkout's
// only markdown, so a Go change is production code. The clone is of the
// pull request's own base branch.
func TestVerify_contractFromBaseCheckout_notFromTheLeaseRead(t *testing.T) {
	t.Parallel()
	pr := verifyPull(changed("internal/verify/classify.go")...)
	pr.BaseRef = "release/1"
	co := verifyCheckout(verifyContract)
	co.branch = "release/1"
	excludeAll := append(append([]byte(nil), contractWithWallClock...), []byte("verify:\n  exclude: [\"**\"]\n")...)
	fx := newVerifyFixture(t, verifyWiring{pr: pr, checkout: co, deps: func(d *forja.Deps) {
		d.Contracts = &fakeContracts{src: domain.ContractSource{SHA: doctorSHA, Body: excludeAll}}
	}})

	wire := wireOf(t, callVerify(t, fx.cs, verifyPR))
	if string(wire["failure_reason"]) != `"inversion_unavailable"` || string(wire["contract_sha"]) != `"`+gitBlobSHA(verifyContract)+`"` {
		t.Fatalf("failure_reason %s contract_sha %s, want the checkout's contract applied", wire["failure_reason"], wire["contract_sha"])
	}
	if fx.cloner.base != "release/1" {
		t.Fatalf("cloned base %q, want the pull request's base release/1", fx.cloner.base)
	}
}

// The run is recorded as forja.verify with each stage timed in order, the
// commits and the contract it was reached on, its verdict and no tokens.
func TestVerify_run_recordedWithStagesShasQAAndNoTokens(t *testing.T) {
	t.Parallel()
	clock := &steppingClock{at: startedAt()}
	fx := newVerifyFixture(t, verifyWiring{pr: markdownPull(), deps: func(d *forja.Deps) { d.Now = clock.now }})

	out := decodeVerify(t, callVerify(t, fx.cs, verifyPR))
	run, err := fx.runs.Get(context.Background(), out.RunID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	assertVerifyRecord(t, run)
	assertVerifyStages(t, statusWire(t, fx.cs, out.RunID))
}

// assertVerifyRecord checks the stored run of the markdown pull request.
func assertVerifyRecord(t *testing.T, run domain.Run) {
	t.Helper()
	wantQA := domain.QA{Verdict: domain.VerdictNotVerifiable, Reason: domain.NotVerifiableOnlyExcludedPaths, Excluded: []string{"README.md", "docs/x.md"}}
	if run.Tool != domain.ToolVerify || run.State != domain.StateSucceeded || run.Issue != implementIssue || !reflect.DeepEqual(run.QA, wantQA) {
		t.Fatalf("run = %+v, want a succeeded forja.verify run of issue 12 with %+v", run, wantQA)
	}
	if run.BaseSHA != planSHA || run.ContractSHA != gitBlobSHA(verifyContract) || run.HeadSHA != verifyHead || run.Tokens != (domain.TokenUsage{}) {
		t.Fatalf("run base %q contract %q head %q tokens %+v", run.BaseSHA, run.ContractSHA, run.HeadSHA, run.Tokens)
	}
	assertFinished(t, run)
}

func assertFinished(t *testing.T, run domain.Run) {
	t.Helper()
	if run.FinishedAt == nil || !run.StartedAt.Before(*run.FinishedAt) {
		t.Fatalf("started %v finished %v, want a finished run", run.StartedAt, run.FinishedAt)
	}
}

// assertVerifyStages checks forja.status lists a verify run's stages in
// order, each timed and succeeded.
func assertVerifyStages(t *testing.T, wire map[string]json.RawMessage) {
	t.Helper()
	stages := stagesOf(t, wire)
	names := make([]string, 0, len(stages))
	for _, s := range stages {
		names = append(names, s.Name)
	}
	if want := []string{"claim", "clone", "diff", "status", "release"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("stages = %q, want %q", names, want)
	}
	assertStagesTimed(t, stages)
}

// The diff and the status run under the contract's wall clock counted
// from the claim, the base checkout's 20 minutes.
func TestVerify_statusUnderRunDeadline(t *testing.T) {
	t.Parallel()
	fx := newVerifyFixture(t, verifyWiring{pr: markdownPull()})

	before := time.Now()
	decodeVerify(t, callVerify(t, fx.cs, verifyPR))
	after := time.Now()
	dl := fx.statuses.deadline()
	if dl.Before(before.Add(20*time.Minute)) || dl.After(after.Add(20*time.Minute)) {
		t.Fatalf("status deadline %v, want 20m from the call, between %v and %v", dl, before.Add(20*time.Minute), after.Add(20*time.Minute))
	}
}

// forja.status shows a verify run's verdict, its reason and the excluded
// paths; a run with no verdict shows the excluded list empty and no
// verdict at all.
func TestStatus_verifyRun_verdictReasonAndExcluded(t *testing.T) {
	t.Parallel()
	fx := newVerifyFixture(t, verifyWiring{pr: markdownPull()})
	out := decodeVerify(t, callVerify(t, fx.cs, verifyPR))

	wire := statusWire(t, fx.cs, out.RunID)
	want := map[string]string{
		"tool": `"forja.verify"`, "verdict": `"not_verifiable"`, "verdict_reason": `"only_excluded_paths"`,
		"excluded": `["README.md","docs/x.md"]`, "head_sha": `"` + verifyHead + `"`,
	}
	for key, value := range want {
		if string(wire[key]) != value {
			t.Errorf("%s = %s, want %s", key, wire[key], value)
		}
	}
	var pr forja.PullRequestRef
	if err := json.Unmarshal(wire["pull_request"], &pr); err != nil || pr != (forja.PullRequestRef{Number: verifyPR, URL: verifyPRURL}) {
		t.Errorf("pull_request = %s, want #%d", wire["pull_request"], verifyPR)
	}
	mustPut(t, fx.runs.Memory, terminalRun("run-plain"))
	plain := statusWire(t, fx.cs, "run-plain")
	if string(plain["excluded"]) != "[]" {
		t.Errorf("excluded = %s on a run with no verdict, want []", plain["excluded"])
	}
	for _, key := range []string{"verdict", "verdict_reason"} {
		if raw, present := plain[key]; present {
			t.Errorf("%s = %s on a run with no verdict", key, raw)
		}
	}
}

// A server missing what forja.verify reaches says which, and touches
// nothing.
func TestVerify_notWired_namesTheMissingAndCallsNothing(t *testing.T) {
	t.Parallel()
	fx := newVerifyFixture(t, verifyWiring{pr: markdownPull(), deps: func(d *forja.Deps) {
		d.PullRequestReader, d.Statuses = nil, nil
	}})

	res := callVerify(t, fx.cs, verifyPR)
	for _, want := range []string{"pull request reader", "statuses"} {
		if !res.IsError || !strings.Contains(textOf(res), want) {
			t.Errorf("IsError %v text %q, want a tool error naming %q", res.IsError, textOf(res), want)
		}
	}
	if strings.Contains(textOf(res), "pusher") || strings.Contains(textOf(res), "pull requests,") || strings.Contains(textOf(res), "identity") {
		t.Errorf("text %q names a dependency forja.verify does not reach", textOf(res))
	}
	if got := fx.calls.list(); len(got) != 0 {
		t.Fatalf("calls = %q, want none", got)
	}
}
