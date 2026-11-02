package mcp

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/github"
)

// ErrNotForjaBranch refuses a correction round on a branch that is not
// the issue's forja/<n>, or whose one open pull request Forja did not open
// from this repository into the base asked for.
var ErrNotForjaBranch = errors.New("not a branch Forja opened")

// maxFinding bounds the review finding a correction round takes, in bytes.
const maxFinding = 64 << 10

// BranchCloner is what a correction round needs to reach the run's branch:
// a checkout pinned at the tip of base, with branch checked out at its own
// tip, owned by the control plane.
type BranchCloner interface {
	CloneBranch(ctx context.Context, repo, base, branch string) (Checkout, error)
}

// Identity is the login the control plane's GitHub credential acts as.
type Identity interface {
	Login(ctx context.Context) (string, error)
}

// round is a correction round: the pull request Forja opened from the
// run's branch, and the finding the agent answers. The finding reaches
// the agent's prompt and nothing else.
type round struct {
	pr      domain.PullRequest
	finding string
}

// openRound is the correction round in asks for, or nil for a first run.
// The input is checked before any call, and the pull request and Forja's
// login are read before anything is reserved; every way the branch fails
// to be one Forja opened wraps ErrNotForjaBranch.
func (h implementer) openRound(ctx context.Context, in ImplementInput) (*round, error) {
	if in.Branch == "" && in.Finding == "" {
		return nil, nil
	}
	if err := roundInput(in); err != nil {
		return nil, err
	}
	pr, err := h.deps.PullRequests.Find(ctx, in.Repository, in.Branch)
	if errors.Is(err, github.ErrNoPullRequest) || errors.Is(err, github.ErrManyPullRequests) {
		return nil, fmt.Errorf("%s: %w: %w", in.Branch, ErrNotForjaBranch, err)
	}
	if err != nil {
		return nil, err
	}
	login, err := h.deps.Identity.Login(ctx)
	if err != nil {
		return nil, err
	}
	if why := notOpenedBy(pr, in, login); why != "" {
		return nil, fmt.Errorf("%s: %w: %s", in.Branch, ErrNotForjaBranch, why)
	}
	return &round{pr: pr, finding: in.Finding}, nil
}

// roundInput refuses a branch without a finding and the reverse, a branch
// other than the issue's own and a finding over maxFinding. Nothing of the
// finding but its size reaches the error.
func roundInput(in ImplementInput) error {
	switch {
	case in.Branch == "" || in.Finding == "":
		return errors.New("a correction round needs both branch and finding")
	case in.Branch != branchPrefix+strconv.Itoa(in.Issue):
		return fmt.Errorf("%q: %w: the issue's branch is %s%d", in.Branch, ErrNotForjaBranch, branchPrefix, in.Issue)
	case len(in.Finding) > maxFinding:
		return fmt.Errorf("finding of %d bytes is over the %d a correction round takes", len(in.Finding), maxFinding)
	}
	return nil
}

// notOpenedBy says why pr is not the pull request Forja, acting as login,
// opened from this repository into the base asked for; empty when it is.
func notOpenedBy(pr domain.PullRequest, in ImplementInput, login string) string {
	switch {
	case login == "" || pr.Author != login:
		return fmt.Sprintf("pull request #%d was opened by %q, not by %q", pr.Number, pr.Author, login)
	case !strings.EqualFold(pr.HeadRepository, in.Repository):
		return fmt.Sprintf("pull request #%d has its head in %q, not in %s", pr.Number, pr.HeadRepository, in.Repository)
	case in.BaseBranch != "" && in.BaseBranch != pr.Base:
		return fmt.Sprintf("pull request #%d targets %q, not %q", pr.Number, pr.Base, in.BaseBranch)
	}
	return ""
}

// withRound makes f a correction round: its base is the pull request's,
// and the pull request is the run's from the start.
func (f *flight) withRound(r *round) {
	if r == nil {
		return
	}
	f.round, f.base, f.run.PullRequest = r, r.pr.Base, r.pr
}

// clone is the clone stage's checkout: the base for a first run, the base
// with the run's branch checked out at its tip for a round.
func (h implementer) clone(ctx context.Context, f *flight) (Checkout, error) {
	if f.round == nil {
		return h.deps.Cloner.Clone(ctx, f.run.Repository, f.base)
	}
	return h.deps.BranchCloner.CloneBranch(ctx, f.run.Repository, f.base, branchPrefix+strconv.Itoa(f.run.Issue))
}

// steps are the stages after the clone: a first run opens its pull
// request, a round comments on the one it found.
func (h implementer) steps(f *flight) []runStep {
	if f.round == nil {
		return []runStep{h.agentStage, h.gatesStage, h.diffStage, h.pushStage, h.pullRequestStage, h.statusStage}
	}
	return []runStep{h.agentStage, h.gatesStage, h.diffStage, h.pushStage, h.commentStage, h.statusStage}
}

// finding is what the agent is handed besides the issue.
func (f *flight) finding() string {
	if f.round == nil {
		return ""
	}
	return f.round.finding
}

// commentStage posts the evidence on the round's pull request and labels
// it when the head needs a human merge, before forja/gates as on a first
// run's pull request.
func (h implementer) commentStage(ctx, done context.Context, f *flight) (bool, error) {
	return f.stage(done, domain.StageComment, func() (bool, error) {
		err := h.deps.PullRequests.Comment(ctx, github.CommentRequest{
			Repository: f.run.Repository, Number: f.run.PullRequest.Number, Body: f.evidence,
		})
		if err != nil {
			return false, err
		}
		return h.labelHumanMerge(ctx, f)
	})
}
