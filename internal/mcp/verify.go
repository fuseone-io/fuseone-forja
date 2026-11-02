package mcp

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// PullRequestReader is what forja.verify needs from GitHub about the pull
// request: its head, its base branch and every file it changes.
type PullRequestReader interface {
	Read(ctx context.Context, repo string, number int) (domain.PullRequestDiff, error)
}

type verifier struct {
	lifecycle
}

// verifyHandler reads the pull request, reserves the issue its branch
// names, classifies its diff under the base commit's contract, inverts a
// diff with production code through the QA agent, publishes forja/qa on
// its head, and releases the reservation. A pull request no run of Forja
// opened, or a refused claim, is a tool error and records no run; a diff
// no inversion could decide is a result.
func verifyHandler(deps Deps) sdk.ToolHandlerFor[VerifyInput, VerifyOutput] {
	v := verifier{lifecycle{deps: deps}}
	return func(ctx context.Context, _ *sdk.CallToolRequest, in VerifyInput) (*sdk.CallToolResult, VerifyOutput, error) {
		out, err := v.verify(ctx, in)
		if err != nil {
			return nil, VerifyOutput{}, fmt.Errorf("%s: %w", domain.ToolVerify, err)
		}
		return nil, out, nil
	}
}

func (v verifier) verify(ctx context.Context, in VerifyInput) (VerifyOutput, error) {
	if err := v.wired(); err != nil {
		return VerifyOutput{}, err
	}
	if in.PullRequest <= 0 {
		return VerifyOutput{}, fmt.Errorf("pull request %d is not positive", in.PullRequest)
	}
	pr, err := v.deps.PullRequestReader.Read(ctx, in.Repository, in.PullRequest)
	if err != nil {
		return VerifyOutput{}, err
	}
	issue, err := runIssue(in.Repository, pr)
	if err != nil {
		return VerifyOutput{}, fmt.Errorf("pull request %d: %w", in.PullRequest, err)
	}
	f, err := v.claim(ctx, runTarget{tool: domain.ToolVerify, repo: in.Repository, issue: issue, base: pr.BaseRef})
	if err != nil {
		return VerifyOutput{}, err
	}
	vf := &verifyFlight{flight: f, pr: pr}
	f.run.HeadSHA, f.run.PullRequest = pr.HeadSHA, domain.PullRequest{Number: pr.Number, URL: pr.URL}
	work := func(ctx, done context.Context, _ *flight) (bool, error) { return v.work(ctx, done, vf) }
	if err := v.run(ctx, f, work); err != nil {
		return VerifyOutput{}, fmt.Errorf("run %s: %w", f.claim.RunID, err)
	}
	return verifyOutput(f.run, f.evidence, v.deps.Prices), nil
}

// wired refuses a server missing any dependency a verify run reaches,
// naming the missing ones, and one without a QA agent with what to set.
func (v verifier) wired() error {
	d := v.deps
	err := notWired([]dependency{
		{"runs", d.Runs != nil}, {"contracts", d.Contracts != nil}, {"claims", d.Claims != nil},
		{"branch cloner", d.BranchCloner != nil}, {"pull request reader", d.PullRequestReader != nil}, {"statuses", d.Statuses != nil},
		{"issues", d.Issues != nil}, {"gates", d.Gates != nil}, {"NewID", d.NewID != nil}, {"Now", d.Now != nil},
	})
	if err != nil {
		return err
	}
	if d.QA == nil {
		return errNoAgent
	}
	return nil
}

// errNotARunBranch is a pull request whose head is not a branch a run of
// Forja pushes in the repository itself.
var errNotARunBranch = errors.New("head is not a forja/<issue> branch of the repository")

// runIssue is the issue a pull request's head names: only a head
// forja/<positive int> in the repository itself, on a known base branch,
// is a run's branch. A fork, or a head whose repository is gone, is not.
func runIssue(repo string, pr domain.PullRequestDiff) (int, error) {
	if pr.HeadRepository != repo || pr.BaseRef == "" {
		return 0, errNotARunBranch
	}
	digits, ok := strings.CutPrefix(pr.HeadRef, branchPrefix)
	n, err := strconv.Atoi(digits)
	if !ok || err != nil || n <= 0 || strconv.Itoa(n) != digits {
		return 0, errNotARunBranch
	}
	return n, nil
}

// verifyOutput is the run as forja.verify answers it, its cost estimated
// at prices.
func verifyOutput(run domain.Run, body string, prices *domain.PriceTable) VerifyOutput {
	return VerifyOutput{
		RunID: run.ID, State: string(run.State),
		Verdict: string(run.QA.Verdict), VerdictReason: string(run.QA.Reason), Excluded: excludedOf(run.QA),
		FailureReason: string(run.Reason),
		BaseSHA:       run.BaseSHA, ContractSHA: run.ContractSHA, HeadSHA: run.HeadSHA,
		PullRequest: pullRequestRef(run.PullRequest), Evidence: body,
		TokensIn: run.Tokens.In(), TokensOut: run.Tokens.Output,
		ComputeMinutes: computeMinutes(run.Compute), EstimatedCostUSDMicros: estimate(prices, run),
		Inversions: inversionReports(run.Inversions),
	}
}

// inversionReports is every candidate on the wire, [] for none.
func inversionReports(invs []domain.Inversion) []InversionReport {
	out := make([]InversionReport, 0, len(invs))
	for _, inv := range invs {
		out = append(out, InversionReport{
			Branch: inv.Branch, Commit: inv.Commit, Paths: append([]string{}, inv.Paths...),
			Admitted: inv.Admitted, Rejected: string(inv.Rejected), Outcome: string(inv.Outcome), FailedStep: inv.Gates.FailedStep,
		})
	}
	return out
}

// excludedOf is the excluded paths, empty rather than nil so the wire
// says [] for none.
func excludedOf(qa domain.QA) []string {
	return append([]string{}, qa.Excluded...)
}
