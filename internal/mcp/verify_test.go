package mcp_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/github"
	forja "github.com/fuseone-io/fuseone-forja/internal/mcp"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

const (
	verifyPR    = 57
	verifyHead  = "9c1b2a3d4e5f60718293a4b5c6d7e8f901234567"
	verifyPRURL = "https://github.com/" + doctorRepo + "/pull/57"
	// The fixed descriptions forja/qa carries, one per reason.
	onlyExcludedDescription = "Not verifiable: only excluded paths changed; a person decides the merge."
	noProductionDescription = "Not verifiable: no production code changed; a person decides the merge."
)

// verifyContract is the base checkout's contract: markdown excluded, the
// tests and the contract itself proof paths, and a wall clock that fits
// the lease sized from contractWithWallClock.
const verifyContract = "version: 1\nimage: golang:1.26\ngates:\n  - name: build\n    run: make build\n" +
	"verify:\n  exclude: [\"**/*.md\"]\nproof_paths:\n  - .forja.yaml\n  - \"**/*_test.go\"\nlimits:\n  wall_clock: 20m\n"

func verifyCheckout(contract string) *fakeCheckout {
	return &fakeCheckout{
		sha: planSHA, branch: "main", dir: "/tmp/forja-verify",
		files: map[string]string{".forja.yaml": contract}, dirs: map[string]bool{},
	}
}

func changed(paths ...string) []domain.ChangedFile {
	out := make([]domain.ChangedFile, 0, len(paths))
	for _, p := range paths {
		out = append(out, domain.ChangedFile{Path: p})
	}
	return out
}

// verifyPull is a pull request implement would have opened for issue 12:
// head forja/12 in the repository itself, into main.
func verifyPull(files ...domain.ChangedFile) domain.PullRequestDiff {
	return domain.PullRequestDiff{
		Number: verifyPR, URL: verifyPRURL, HeadRepository: doctorRepo,
		HeadRef: "forja/12", HeadSHA: verifyHead, BaseRef: "main", Files: files,
	}
}

type verifyFixture struct {
	calls    *callLog
	claims   *fakeClaims
	runs     *recordingRuns
	cloner   *fakeCloner
	pulls    *fakePullReader
	statuses *fakeStatuses
	cs       *sdk.ClientSession
}

// verifyWiring changes the fixture before the server is connected.
type verifyWiring struct {
	pr       domain.PullRequestDiff
	checkout *fakeCheckout
	claims   func(*fakeClaims)
	deps     func(*forja.Deps)
}

// newVerifyFixture is a server wired with what forja.verify reaches and
// nothing else: no agent, gates, pusher, pull request opener nor issue
// reader, so a verify that touched one would crash. The API's contract,
// read only to size the lease, is contractWithWallClock; the base
// checkout holds verifyContract unless the wiring says otherwise.
func newVerifyFixture(t *testing.T, w verifyWiring) *verifyFixture {
	t.Helper()
	calls := &callLog{}
	claims := &fakeClaims{calls: calls}
	if w.claims != nil {
		w.claims(claims)
	}
	co := w.checkout
	if co == nil {
		co = verifyCheckout(verifyContract)
	}
	runs := &recordingRuns{Memory: runstore.NewMemory(), calls: calls}
	deps := testDeps(runs, &fakeContracts{src: domain.ContractSource{SHA: doctorSHA, Body: contractWithWallClock}}, claims)
	fx := &verifyFixture{
		calls: calls, claims: claims, runs: runs,
		cloner:   &fakeCloner{co: co, log: calls},
		pulls:    &fakePullReader{calls: calls, repo: doctorRepo, prs: map[int]domain.PullRequestDiff{verifyPR: w.pr}},
		statuses: &fakeStatuses{deliveryLog[github.StatusRequest]{calls: calls, name: "status"}},
	}
	deps.Cloner, deps.PullRequestReader, deps.Statuses = fx.cloner, fx.pulls, fx.statuses
	deps.Agent, deps.Gates, deps.Pusher, deps.PullRequests, deps.Issues = nil, nil, nil, nil, nil
	if w.deps != nil {
		w.deps(&deps)
	}
	fx.cs = connectDeps(t, deps)
	return fx
}

func callVerify(t *testing.T, cs *sdk.ClientSession, pr int) *sdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      string(domain.ToolVerify),
		Arguments: map[string]any{"repository": doctorRepo, "pull_request": pr},
	})
	if err != nil {
		t.Fatalf("CallTool(forja.verify): protocol error %v", err)
	}
	return res
}

func decodeVerify(t *testing.T, res *sdk.CallToolResult) forja.VerifyOutput {
	t.Helper()
	if res.IsError {
		t.Fatalf("IsError = true, content: %s", textOf(res))
	}
	var out forja.VerifyOutput
	if err := json.Unmarshal(structuredJSON(t, res), &out); err != nil {
		t.Fatalf("decode VerifyOutput: %v", err)
	}
	return out
}

// AC1: a pull request that only changes markdown gets forja/qa neutral on
// its head and the verdict not verifiable with the excluded paths.
func TestVerify_onlyMarkdownChanged_qaPendingWithExcludedPaths(t *testing.T) {
	t.Parallel()
	fx := newVerifyFixture(t, verifyWiring{pr: verifyPull(changed("README.md", "docs/x.md")...)})

	out := decodeVerify(t, callVerify(t, fx.cs, verifyPR))
	want := github.StatusRequest{
		Repository: doctorRepo, SHA: verifyHead, State: github.StatePending, Context: "forja/qa",
		Description: onlyExcludedDescription, TargetURL: verifyPRURL,
	}
	if got := fx.statuses.got(); len(got) != 1 || got[0] != want {
		t.Fatalf("statuses = %+v, want exactly [%+v]", got, want)
	}
	if out.Verdict != "not_verifiable" || out.VerdictReason != "only_excluded_paths" || out.State != "succeeded" {
		t.Fatalf("verdict %q reason %q state %q, want not_verifiable, only_excluded_paths, succeeded", out.Verdict, out.VerdictReason, out.State)
	}
	if !reflect.DeepEqual(out.Excluded, []string{"README.md", "docs/x.md"}) {
		t.Fatalf("excluded = %q, want [README.md docs/x.md]", out.Excluded)
	}
	assertVerifyPins(t, out)
}

// assertVerifyPins checks the output names what the verdict was reached
// on: the base commit, the contract blob there, the head and the pull
// request, with the evidence pinning them too, and no tokens spent.
func assertVerifyPins(t *testing.T, out forja.VerifyOutput) {
	t.Helper()
	contractSHA := gitBlobSHA(verifyContract)
	if out.RunID != "run-1" || out.BaseSHA != planSHA || out.ContractSHA != contractSHA || out.HeadSHA != verifyHead {
		t.Fatalf("run %q base %q contract %q head %q, want run-1, %s, %s, %s", out.RunID, out.BaseSHA, out.ContractSHA, out.HeadSHA, planSHA, contractSHA, verifyHead)
	}
	if out.PullRequest == nil || *out.PullRequest != (forja.PullRequestRef{Number: verifyPR, URL: verifyPRURL}) {
		t.Fatalf("pull_request = %+v, want #%d %s", out.PullRequest, verifyPR, verifyPRURL)
	}
	if out.TokensIn != 0 || out.TokensOut != 0 {
		t.Fatalf("tokens %d/%d, want 0/0: no model was called", out.TokensIn, out.TokensOut)
	}
	assertEvidencePins(t, out.Evidence, contractSHA)
}

func assertEvidencePins(t *testing.T, evidence, contractSHA string) {
	t.Helper()
	for _, want := range []string{"Refs #12", planSHA, contractSHA, verifyHead, "run-1"} {
		if !strings.Contains(evidence, want) {
			t.Errorf("evidence does not pin %q:\n%s", want, evidence)
		}
	}
}

// AC2: whatever the diff, this slice never publishes forja/qa success. A
// diff that is not verifiable gets one pending status with its reason's
// description; a diff with production code gets none, and the run fails
// with no verdict.
func TestVerify_anyDiff_neverPublishesQASuccess(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		files        []domain.ChangedFile
		wantDesc     string
		wantVerdict  string
		wantReason   string
		wantExcluded string
	}{
		{name: "markdown only", files: changed("README.md", "docs/x.md"), wantDesc: onlyExcludedDescription, wantVerdict: "not_verifiable", wantReason: "only_excluded_paths", wantExcluded: `["README.md","docs/x.md"]`},
		{name: "proof paths only", files: changed("internal/mcp/verify_test.go", ".forja.yaml"), wantDesc: noProductionDescription, wantVerdict: "not_verifiable", wantReason: "no_production_code", wantExcluded: `[]`},
		{name: "proof and excluded", files: changed("README.md", "internal/mcp/verify_test.go"), wantDesc: noProductionDescription, wantVerdict: "not_verifiable", wantReason: "no_production_code", wantExcluded: `["README.md"]`},
		{name: "empty diff", files: nil, wantDesc: noProductionDescription, wantVerdict: "not_verifiable", wantReason: "no_production_code", wantExcluded: `[]`},
		{name: "rename from production to excluded", files: []domain.ChangedFile{{Path: "docs/verify.md", PreviousPath: "internal/verify/classify.go"}}, wantExcluded: `["docs/verify.md"]`},
		{name: "production diff", files: changed("internal/verify/classify.go"), wantExcluded: `[]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fx := newVerifyFixture(t, verifyWiring{pr: verifyPull(tt.files...)})

			res := callVerify(t, fx.cs, verifyPR)
			wire := wireOf(t, res)
			for _, s := range fx.statuses.got() {
				if s.State == github.StateSuccess || s.State != github.StatePending || s.Context != "forja/qa" {
					t.Fatalf("published %+v; forja/qa may only be pending here", s)
				}
			}
			if string(wire["excluded"]) != tt.wantExcluded {
				t.Errorf("excluded = %s, want %s", wire["excluded"], tt.wantExcluded)
			}
			if tt.wantVerdict == "" {
				assertProductionRun(t, fx, res, wire)
				return
			}
			assertNotVerifiable(t, fx, decodeVerify(t, res), tt.wantDesc, tt.wantVerdict, tt.wantReason)
		})
	}
}

func assertNotVerifiable(t *testing.T, fx *verifyFixture, out forja.VerifyOutput, desc, verdict, reason string) {
	t.Helper()
	if got := fx.statuses.got(); len(got) != 1 || got[0].Description != desc || got[0].SHA != verifyHead {
		t.Fatalf("statuses = %+v, want one on %s saying %q", got, verifyHead, desc)
	}
	if out.State != "succeeded" || out.Verdict != verdict || out.VerdictReason != reason || out.FailureReason != "" {
		t.Fatalf("state %q verdict %q reason %q failure %q, want succeeded %s %s", out.State, out.Verdict, out.VerdictReason, out.FailureReason, verdict, reason)
	}
}

// assertProductionRun checks a diff with production code published no
// status and failed, as a result and not a tool error, with no verdict on
// the wire.
func assertProductionRun(t *testing.T, fx *verifyFixture, res *sdk.CallToolResult, wire map[string]json.RawMessage) {
	t.Helper()
	if res.IsError {
		t.Fatalf("IsError = true for a production diff: %s", textOf(res))
	}
	if got := fx.statuses.got(); len(got) != 0 {
		t.Fatalf("statuses = %+v, want none for a production diff", got)
	}
	if string(wire["state"]) != `"failed"` || string(wire["failure_reason"]) != `"inversion_unavailable"` {
		t.Fatalf("state %s failure_reason %s, want failed and inversion_unavailable", wire["state"], wire["failure_reason"])
	}
	for _, key := range []string{"verdict", "verdict_reason"} {
		if raw, present := wire[key]; present {
			t.Errorf("%s = %s on the wire of a run that reached no verdict", key, raw)
		}
	}
}

// The keys that are always there are there, even when empty, and nothing
// is null.
func TestVerify_wireKeys_alwaysPresentAndSnakeCase(t *testing.T) {
	t.Parallel()
	fx := newVerifyFixture(t, verifyWiring{pr: verifyPull(changed("internal/verify/classify.go")...)})

	wire := wireOf(t, callVerify(t, fx.cs, verifyPR))
	want := map[string]string{"run_id": `"run-1"`, "state": `"failed"`, "excluded": `[]`, "tokens_in": "0", "tokens_out": "0"}
	for key, value := range want {
		if string(wire[key]) != value {
			t.Errorf("%s = %s, want %s", key, wire[key], value)
		}
	}
	for _, key := range []string{"failure_reason", "base_sha", "contract_sha", "head_sha", "pull_request", "evidence"} {
		if _, present := wire[key]; !present {
			t.Errorf("%s absent on the wire of a run that knows it", key)
		}
	}
	if len(wire) != len(want)+6 {
		t.Errorf("wire has %d keys, want %d: %s", len(wire), len(want)+6, mustJSON(t, wire))
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}
