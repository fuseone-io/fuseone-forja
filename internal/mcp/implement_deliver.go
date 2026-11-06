package mcp

import (
	"context"
	"errors"
	"strconv"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/evidence"
	"github.com/fuseone-io/fuseone-forja/internal/github"
)

// Pusher is what forja.implement needs to send the agent's head to the
// run's branch, from the control plane's clone.
type Pusher interface {
	Push(ctx context.Context, req github.PushRequest) error
}

// PullRequests is what forja.implement needs to open the run's pull
// request and mark it for a human merge, and for a correction round to
// find the one open pull request of the run's branch and comment on it.
type PullRequests interface {
	Open(ctx context.Context, req github.PullRequestRequest) (domain.PullRequest, error)
	Label(ctx context.Context, req github.LabelRequest) error
	Find(ctx context.Context, repo, branch string) (domain.PullRequest, error)
	Comment(ctx context.Context, req github.CommentRequest) error
}

// CommitStatuses is what forja.implement needs to publish forja/gates.
type CommitStatuses interface {
	Publish(ctx context.Context, req github.StatusRequest) error
}

// gatesContext is the commit status a green run publishes on its head.
const gatesContext = "forja/gates"

// gatesDescription is the status's one line; GitHub keeps it short.
const gatesDescription = "Every gate of the base commit's contract passed on this commit."

// humanMergeLabel marks a pull request whose head changed the proof or a
// human-merge path.
const humanMergeLabel = "forja/human-merge"

// gatesStage runs the base checkout's contract, parsed before the agent
// ran, on the agent's head in a fresh workspace, records the attempt and
// renders the evidence. A red gate ends the attempt without an error; a
// runner that failed still charges the run its workspace's compute.
func (h implementer) gatesStage(ctx, done context.Context, f *flight) (bool, error) {
	return f.stage(ctx, done, domain.StageGates, func() (bool, error) {
		report, err := h.deps.Gates.Run(ctx, f.run.ID, f.contract, f.co.Dir())
		f.charge(report.Compute, domain.TokenUsage{})
		if err != nil {
			return false, err
		}
		f.run.Gates, f.run.Registry = report, f.run.Registry.Merge(report.Registry)
		f.run.Egress = f.run.Egress.Merge(report.Egress)
		f.run.Attempts = append(f.run.Attempts, domain.Attempt{Number: f.attempt, HeadSHA: f.run.HeadSHA, Gates: report, Tokens: f.spent})
		f.evidence = evidence.Render(h.evidenceOf(f))
		if !report.OK() {
			f.run.Reason = domain.ReasonGateFailed
			return false, nil
		}
		return true, nil
	})
}

func (h implementer) evidenceOf(f *flight) evidence.Run {
	r := evidence.Run{
		Issue: f.run.Issue, RunID: f.run.ID, BaseBranch: f.co.Branch(),
		BaseSHA: f.run.BaseSHA, ContractSHA: f.run.ContractSHA, StartSHA: f.run.StartSHA, HeadSHA: f.run.HeadSHA,
		Gates: f.run.Gates, Tokens: f.run.Tokens, Elapsed: h.deps.Now().Sub(f.run.StartedAt),
		Attempts: f.run.Attempts, MaxAttempts: f.run.MaxAttempts,
		Limits: f.run.Limits, Proof: f.run.Proof,
	}
	if f.run.DiffLines != nil {
		r.DiffLines = *f.run.DiffLines
	}
	return r
}

// diffStage holds back a head that changes more lines against the base
// than the contract allows: nothing is pushed, opened or published, and
// the run fails without an error. The limit itself passes.
func (h implementer) diffStage(ctx, done context.Context, f *flight) (bool, error) {
	return f.stage(ctx, done, domain.StageDiff, func() (bool, error) {
		if f.run.DiffLines == nil {
			return false, errors.New("diff of the head was never counted")
		}
		if *f.run.DiffLines > f.run.Limits.MaxDiffLines {
			f.run.Reason = domain.ReasonDiffTooLarge
			return false, nil
		}
		return true, nil
	})
}

// pushStage sends the head to the run's branch from the control plane's
// clone.
func (h implementer) pushStage(ctx, done context.Context, f *flight) (bool, error) {
	return f.stage(ctx, done, domain.StagePush, func() (bool, error) {
		err := h.deps.Pusher.Push(ctx, github.PushRequest{
			Repository: f.run.Repository, Dir: f.co.Dir(), Head: f.run.HeadSHA, Branch: f.run.Branch,
		})
		return err == nil, err
	})
}

// pullRequestStage opens the pull request from the run's branch into the
// base branch, with the evidence as its body, and labels it when it needs
// a human merge. The label comes before forja/gates: a failed label fails
// the stage, so no green status ever sits on an unmarked pull request.
func (h implementer) pullRequestStage(ctx, done context.Context, f *flight) (bool, error) {
	return f.stage(ctx, done, domain.StagePullRequest, func() (bool, error) {
		pr, err := h.deps.PullRequests.Open(ctx, github.PullRequestRequest{
			Repository: f.run.Repository, Title: pullRequestTitle(f.issue), Head: f.run.Branch, Base: f.co.Branch(), Body: f.evidence,
		})
		if err != nil {
			return false, err
		}
		f.run.PullRequest = pr
		return h.labelHumanMerge(ctx, f)
	})
}

// labelHumanMerge marks the run's pull request when its head needs a human
// merge; a failed label fails the stage that called it.
func (h implementer) labelHumanMerge(ctx context.Context, f *flight) (bool, error) {
	if !f.run.Proof.HumanMerge() {
		return true, nil
	}
	err := h.deps.PullRequests.Label(ctx, github.LabelRequest{
		Repository: f.run.Repository, Number: f.run.PullRequest.Number, Labels: []string{humanMergeLabel},
	})
	return err == nil, err
}

// statusStage publishes forja/gates success on the head, pointing at the
// pull request that carries the evidence.
func (h implementer) statusStage(ctx, done context.Context, f *flight) (bool, error) {
	return f.stage(ctx, done, domain.StageStatus, func() (bool, error) {
		err := h.deps.Statuses.Publish(ctx, github.StatusRequest{
			Repository: f.run.Repository, SHA: f.run.HeadSHA, State: github.StateSuccess,
			Context: gatesContext, Description: gatesDescription, TargetURL: f.run.PullRequest.URL,
		})
		return err == nil, err
	})
}

// pullRequestTitle is the issue's title, or a title naming it when it has
// none.
func pullRequestTitle(issue domain.Issue) string {
	if issue.Title != "" {
		return issue.Title
	}
	return "Forja: issue #" + strconv.Itoa(issue.Number)
}

// implementOutput is the run as forja.implement answers it, its cost
// estimated at prices.
func implementOutput(run domain.Run, body string, prices *domain.PriceTable) ImplementOutput {
	return ImplementOutput{
		RunID: run.ID, State: string(run.State), FailureReason: string(run.Reason), FailedStep: run.Gates.FailedStep,
		BaseSHA: run.BaseSHA, ContractSHA: run.ContractSHA, HeadSHA: run.HeadSHA, Branch: run.Branch,
		PullRequest: pullRequestRef(run.PullRequest), Evidence: body,
		WallClockSeconds: run.Limits.WallClock.Seconds(), MaxDiffLines: run.Limits.MaxDiffLines, DiffLines: run.DiffLines,
		TokensIn: run.Tokens.In(), TokensOut: run.Tokens.Output,
		ComputeMinutes: computeMinutes(run.Compute), EstimatedCostUSDMicros: estimate(prices, run),
		Setup: stepReports(run.Gates.Setup), Gates: stepReports(run.Gates.Gates),
		Attempt: currentAttempt(run.Stages), MaxAttempts: run.MaxAttempts, Attempts: attemptReports(run.Attempts),
		HumanMerge: run.Proof.HumanMerge(), ProofApparatus: changedPaths(run.Proof.ProofPaths), HumanMergePaths: changedPaths(run.Proof.HumanMergePaths),
	}
}

func pullRequestRef(pr domain.PullRequest) *PullRequestRef {
	if pr.Number == 0 {
		return nil
	}
	return &PullRequestRef{Number: pr.Number, URL: pr.URL}
}
