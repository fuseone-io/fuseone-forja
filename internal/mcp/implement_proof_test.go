package mcp_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/evidence"
	"github.com/fuseone-io/fuseone-forja/internal/github"
	forja "github.com/fuseone-io/fuseone-forja/internal/mcp"
)

// proofContract is the base checkout's contract with the pilot's lists.
const proofContract = checkoutContract +
	"proof_paths:\n  - .forja.yaml\n  - Makefile\n  - .golangci.yml\n  - \"**/testdata/**\"\n  - \"**/*_test.go\"\n" +
	"human_merge_paths:\n  - \".github/**\"\n"

var (
	makefileChanged  = domain.FileChange{Path: "Makefile", Kind: domain.ChangeModified, Added: 3, Deleted: 1}
	codeownersAdded  = domain.FileChange{Path: ".github/CODEOWNERS", Kind: domain.ChangeAdded, Added: 2}
	newTestFile      = domain.FileChange{Path: "pkg/new_test.go", Kind: domain.ChangeAdded, Added: 9}
	newTestdataFile  = domain.FileChange{Path: "pkg/testdata/f", Kind: domain.ChangeAdded, Added: 1}
	productionChange = domain.FileChange{Path: "internal/x.go", Kind: domain.ChangeModified, Added: 5, Deleted: 5}
)

// newProofFixture is the implement fixture whose base contract carries the
// pilot's proof_paths and human_merge_paths, and whose agent's head
// changes the given paths against the base.
func newProofFixture(t *testing.T, changes ...domain.FileChange) *implementFixture {
	t.Helper()
	fx := newImplementFixture(t, contractWithWallClock, nil)
	fx.cloner.co.files[".forja.yaml"] = proofContract
	fx.cloner.co.changes = changes
	return fx
}

func changedPath(c domain.FileChange) forja.ChangedPath {
	return forja.ChangedPath{Path: c.Path, Change: string(c.Kind), Added: c.Added, Deleted: c.Deleted, Binary: c.Binary}
}

// proofOnWire is what a wire object says of the human merge.
type proofOnWire struct {
	HumanMerge      bool                `json:"human_merge"`
	ProofApparatus  []forja.ChangedPath `json:"proof_apparatus"`
	HumanMergePaths []forja.ChangedPath `json:"human_merge_paths"`
}

func decodeProof(t *testing.T, wire map[string]json.RawMessage) proofOnWire {
	t.Helper()
	for _, key := range []string{"human_merge", "proof_apparatus", "human_merge_paths"} {
		if _, ok := wire[key]; !ok || string(wire[key]) == "null" {
			t.Fatalf("%s absent or null on the wire: %s", key, wire[key])
		}
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var p proofOnWire
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("decode proof: %v", err)
	}
	return p
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

// The classification of the agent's head reaches every reader the same:
// forja.implement, forja.status and the evidence the pull request carries.
func TestImplement_proofChanges_listedOnOutputStatusAndEvidence(t *testing.T) {
	t.Parallel()
	fx := newProofFixture(t, productionChange, makefileChanged, newTestFile, codeownersAdded)

	wire := implementWire(t, fx)
	want := proofOnWire{HumanMerge: true, ProofApparatus: []forja.ChangedPath{changedPath(makefileChanged)}, HumanMergePaths: []forja.ChangedPath{changedPath(codeownersAdded)}}
	if got := decodeProof(t, wire); !reflect.DeepEqual(got, want) {
		t.Fatalf("forja.implement proof = %+v, want %+v", got, want)
	}
	if got := decodeProof(t, statusOf(t, fx)); !reflect.DeepEqual(got, want) {
		t.Fatalf("forja.status proof = %+v, want %+v", got, want)
	}
	wantBody := evidence.Render(evidence.Run{
		Issue: implementIssue, RunID: "run-1", BaseBranch: "main",
		BaseSHA: planSHA, ContractSHA: gitBlobSHA(proofContract), HeadSHA: agentHead,
		Gates: greenReport(), Tokens: spent,
		Proof: domain.Proof{ProofPaths: []domain.FileChange{makefileChanged}, HumanMergePaths: []domain.FileChange{codeownersAdded}},
	})
	if prs := fx.prs.got(); len(prs) != 1 || prs[0].Body != wantBody {
		t.Fatalf("pull request bodies = %+v, want one with the proof sections:\n%s", prs, wantBody)
	}
	if !reflect.DeepEqual(fx.cloner.co.diffed, []string{agentHead}) {
		t.Fatalf("diffed %q, want the adopted head %s once", fx.cloner.co.diffed, agentHead)
	}
}

// A run whose agent changed no listed path says so on the wire with empty
// lists, never null, and its evidence carries neither section.
func TestImplement_newTestFileOnly_noSectionNoLabel(t *testing.T) {
	t.Parallel()
	fx := newProofFixture(t, newTestFile, newTestdataFile, productionChange)

	wire := implementWire(t, fx)
	for name, w := range map[string]map[string]json.RawMessage{"forja.implement": wire, "forja.status": statusOf(t, fx)} {
		if string(w["human_merge"]) != "false" || string(w["proof_apparatus"]) != "[]" || string(w["human_merge_paths"]) != "[]" {
			t.Fatalf("%s: human_merge %s proof_apparatus %s human_merge_paths %s, want false, [] and []", name, w["human_merge"], w["proof_apparatus"], w["human_merge_paths"])
		}
	}
	body := fx.prs.got()[0].Body
	for _, absent := range []string{"Human merge required", "## Proof apparatus", "## Human merge paths"} {
		if strings.Contains(body, absent) {
			t.Fatalf("body carries %q:\n%s", absent, body)
		}
	}
	if got := fx.prs.labels.got(); len(got) != 0 {
		t.Fatalf("labels = %+v, want none", got)
	}
	assertDeliveryOrder(t, fx, "push", "pull_request", "status")
}

// assertDeliveryOrder checks the delivery calls of the run, in order.
func assertDeliveryOrder(t *testing.T, fx *implementFixture, want ...string) {
	t.Helper()
	var got []string
	for _, c := range fx.calls.list() {
		switch c {
		case "push", "pull_request", "label", "status":
			got = append(got, c)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("delivery calls = %q, want %q", got, want)
	}
}

func assertHumanMergeLabel(t *testing.T, fx *implementFixture) {
	t.Helper()
	want := []github.LabelRequest{{Repository: doctorRepo, Number: 41, Labels: []string{"forja/human-merge"}}}
	if got := fx.prs.labels.got(); !reflect.DeepEqual(got, want) {
		t.Fatalf("labels = %+v, want %+v", got, want)
	}
	assertDeliveryOrder(t, fx, "push", "pull_request", "label", "status")
}

// AC1: the Makefile is proof. The pull request carries the section and the
// label, applied before forja/gates, which stays the gates' alone.
func TestImplement_agentChangedMakefile_proofSectionLabelAndGatesStatusUnchanged(t *testing.T) {
	t.Parallel()
	fx := newProofFixture(t, makefileChanged)

	out := decodeImplement(t, callImplement(t, fx.cs, implementIssue))
	assertHumanMergeLabel(t, fx)
	body := fx.prs.got()[0].Body
	if !strings.Contains(body, "\n## Proof apparatus\n") || !strings.Contains(body, "| `Makefile` | modified | +3 -1 |") {
		t.Fatalf("body lacks the Makefile in the proof apparatus:\n%s", body)
	}
	wantStatus := []github.StatusRequest{{
		Repository: doctorRepo, SHA: agentHead, State: github.StateSuccess, Context: "forja/gates",
		Description: "Every gate of the base commit's contract passed on this commit.", TargetURL: prURL,
	}}
	if got := fx.statuses.got(); !reflect.DeepEqual(got, wantStatus) {
		t.Fatalf("statuses = %+v, want %+v", got, wantStatus)
	}
	if out.State != "succeeded" || !out.HumanMerge {
		t.Fatalf("state %q human_merge %v, want succeeded and marked", out.State, out.HumanMerge)
	}
}

// AC3: a change under .github/** marks the human merge without touching
// proof_paths: label and its own section, no proof section.
func TestImplement_githubDirChanged_labelWithoutProofSection(t *testing.T) {
	t.Parallel()
	fx := newProofFixture(t, codeownersAdded, productionChange)

	out := decodeImplement(t, callImplement(t, fx.cs, implementIssue))
	assertHumanMergeLabel(t, fx)
	body := fx.prs.got()[0].Body
	if !strings.Contains(body, "\n## Human merge paths\n") || strings.Contains(body, "## Proof apparatus") {
		t.Fatalf("body, want the human merge section only:\n%s", body)
	}
	if !out.HumanMerge || len(out.ProofApparatus) != 0 {
		t.Fatalf("human_merge %v proof_apparatus %+v, want marked with no proof", out.HumanMerge, out.ProofApparatus)
	}
	if got := decodeProof(t, statusOf(t, fx)); !got.HumanMerge || !reflect.DeepEqual(got.HumanMergePaths, []forja.ChangedPath{changedPath(codeownersAdded)}) {
		t.Fatalf("forja.status proof = %+v, want marked by CODEOWNERS", got)
	}
}

// Fail closed: a pull request that needs a human merge never gets a green
// forja/gates without its label. The run fails at the pull request stage,
// the pull request it opened still recorded, and the issue is freed.
func TestImplement_labelRefused_noStatusRunFailedClaimReleased(t *testing.T) {
	t.Parallel()
	fx := newProofFixture(t, makefileChanged)
	fx.prs.labels.err = errors.New("label down")

	res := callImplement(t, fx.cs, implementIssue)
	if !res.IsError || !strings.Contains(textOf(res), "label down") {
		t.Fatalf("IsError %v text %q, want the label's failure as a tool error", res.IsError, textOf(res))
	}
	if got := fx.statuses.got(); len(got) != 0 {
		t.Fatalf("statuses = %+v, want none", got)
	}
	assertFailedAt(t, fx, "pull_request")
	if released := fx.claims.releasedClaims(); len(released) != 1 {
		t.Fatalf("released %d claims, want 1", len(released))
	}
	if wire := statusOf(t, fx); !strings.Contains(string(wire["pull_request"]), `"number":41`) {
		t.Fatalf("pull_request on forja.status = %s, want the one opened", wire["pull_request"])
	}
}

// Rule 2: the lists are the base contract's. An agent that empties them in
// its own .forja.yaml still has its change to the contract and the
// Makefile classified by the base's.
func TestImplement_agentEmptiedProofPathsInContract_baseListsStillApply(t *testing.T) {
	t.Parallel()
	contractChanged := domain.FileChange{Path: ".forja.yaml", Kind: domain.ChangeModified, Added: 2, Deleted: 7}
	fx := newProofFixture(t, contractChanged, makefileChanged)
	fx.cloner.co.adoptedDir = t.TempDir()
	emptied := "version: 1\nimage: golang:1.26\ngates:\n  - name: build\n    run: 'true'\nproof_paths: []\nhuman_merge_paths: []\n"
	if err := os.WriteFile(filepath.Join(fx.cloner.co.adoptedDir, ".forja.yaml"), []byte(emptied), 0o644); err != nil {
		t.Fatal(err)
	}

	got := decodeProof(t, implementWire(t, fx))
	want := proofOnWire{HumanMerge: true, ProofApparatus: []forja.ChangedPath{changedPath(contractChanged), changedPath(makefileChanged)}, HumanMergePaths: []forja.ChangedPath{}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("proof = %+v, want the base lists applied: %+v", got, want)
	}
}

// The diff is part of the adoption: a diff that fails is an infrastructure
// error of the agent stage, before any gate.
func TestImplement_diffFails_toolErrorNoGates(t *testing.T) {
	t.Parallel()
	fx := newProofFixture(t, makefileChanged)
	fx.cloner.co.diffErr = errors.New("diff down")

	res := callImplement(t, fx.cs, implementIssue)
	if !res.IsError || !strings.Contains(textOf(res), "diff down") {
		t.Fatalf("IsError %v text %q, want the diff's failure as a tool error", res.IsError, textOf(res))
	}
	if fx.gates.calls != 0 || len(fx.pusher.got()) != 0 {
		t.Fatalf("gates ran %d times, pushes %d, want none", fx.gates.calls, len(fx.pusher.got()))
	}
	assertFailedAt(t, fx, "agent")
	if released := fx.claims.releasedClaims(); len(released) != 1 {
		t.Fatalf("released %d claims, want 1", len(released))
	}
}

// A red run delivers nothing, and its evidence and forja.status still show
// what of the proof the agent changed.
func TestImplement_gateRed_proofStillReported(t *testing.T) {
	t.Parallel()
	fx := newProofFixture(t, makefileChanged)
	fx.gates.report = redReport()

	out := decodeImplement(t, callImplement(t, fx.cs, implementIssue))
	if out.State != "failed" || !out.HumanMerge || !reflect.DeepEqual(out.ProofApparatus, []forja.ChangedPath{changedPath(makefileChanged)}) {
		t.Fatalf("state %q human_merge %v proof_apparatus %+v, want failed with the Makefile", out.State, out.HumanMerge, out.ProofApparatus)
	}
	if !strings.Contains(out.Evidence, "## Proof apparatus") {
		t.Fatalf("red evidence lacks the proof section:\n%s", out.Evidence)
	}
	if got := decodeProof(t, statusOf(t, fx)); !got.HumanMerge {
		t.Fatal("forja.status of the red run does not mark the human merge")
	}
	if n := len(fx.pusher.got()) + len(fx.prs.got()) + len(fx.statuses.got()); n != 0 {
		t.Fatalf("a red gate made %d delivery calls, want none", n)
	}
}
