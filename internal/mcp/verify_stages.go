package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/fuseone-io/fuseone-forja/internal/agent"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/evidence"
	"github.com/fuseone-io/fuseone-forja/internal/github"
	"github.com/fuseone-io/fuseone-forja/internal/verify"
)

// qaContext is the commit status a verify run publishes on the head.
const qaContext = "forja/qa"

// The fixed descriptions of forja/qa: one per reason a diff is not
// verifiable, and one for a verdict that bites. GitHub keeps each under
// 140 characters.
const (
	qaOnlyExcludedDescription = "Not verifiable: only excluded paths changed; a person decides the merge."
	qaNoProductionDescription = "Not verifiable: no production code changed; a person decides the merge."
	qaBitesDescription        = "Bites: every inversion turned a gate red."
)

// errHeadMoved is a pull request whose head is no longer the commit it was
// read at.
var errHeadMoved = errors.New("the pull request's head moved after it was read")

// verifyFlight is a verify run in progress: the run's flight, the pull
// request it verifies, read before the claim, and what its stages leave
// for the next.
type verifyFlight struct {
	*flight
	pr domain.PullRequestDiff
	// classified says the diff stage ran; production that it found
	// production code.
	classified, production bool
	// candidates are the QA agent's branches with their bundles.
	candidates []agent.Candidate
}

// verifyStep is one stage of a verify run after the clone.
type verifyStep func(ctx, done context.Context, f *verifyFlight) (bool, error)

// work clones the base with the pull request's head at its tip and reads
// the base's contract, then classifies the diff, inverts it when it has
// production code, and publishes forja/qa, under one deadline: the
// contract's wall clock counted from the claim. The evidence is rendered
// once the diff was classified, whatever ended the run.
func (v verifier) work(ctx, done context.Context, f *verifyFlight) (bool, error) {
	defer f.closeCheckout()
	if ok, err := v.cloneStage(ctx, done, f); !ok || err != nil {
		return false, err
	}
	runCtx, cancel := context.WithTimeoutCause(ctx, f.left, errWallClock)
	defer cancel()
	ok, err := v.steps(runCtx, done, f)
	if f.classified {
		f.evidence = evidence.RenderVerify(evidenceOfVerify(f))
	}
	return ok, err
}

func (v verifier) steps(runCtx, done context.Context, f *verifyFlight) (bool, error) {
	for _, step := range []verifyStep{v.diffStage, v.inversions, v.statusStage} {
		if ok, err := step(runCtx, done, f); !ok || err != nil {
			return false, stopped(runCtx, f.flight, err)
		}
	}
	return true, nil
}

// cloneStage clones the pull request's base branch with its head checked
// out at the tip, refuses a tip that is not the head the pull request was
// read at, reads the contract at the base commit and sizes what is left of
// the wall clock.
func (v verifier) cloneStage(ctx, done context.Context, f *verifyFlight) (bool, error) {
	return f.stage(done, domain.StageClone, func() (bool, error) {
		co, err := v.deps.BranchCloner.CloneBranch(ctx, f.run.Repository, f.base, f.pr.HeadRef)
		if err != nil {
			return false, err
		}
		f.co, f.run.BaseSHA = co, co.SHA()
		if co.Start() != f.pr.HeadSHA {
			return false, fmt.Errorf("%w: %s is at %s, read at %s", errHeadMoved, f.pr.HeadRef, co.Start(), f.pr.HeadSHA)
		}
		parsed, contractSHA, err := readContract(ctx, co, f.run.Repository)
		if err != nil {
			return false, err
		}
		f.contract, f.run.ContractSHA = parsed, contractSHA
		if f.left, err = v.timeLeft(f.claim, parsed); err != nil {
			return false, fmt.Errorf("contract at %s: %w", co.SHA(), err)
		}
		return true, nil
	})
}

// diffStage classifies the changed paths under the base contract.
func (v verifier) diffStage(_, done context.Context, f *verifyFlight) (bool, error) {
	return f.stage(done, domain.StageDiff, func() (bool, error) {
		outcome := verify.Classify(f.contract, f.pr.Files)
		f.run.QA, f.production, f.classified = outcome.QA, outcome.Production, true
		return true, nil
	})
}

// statusStage publishes forja/qa on the head with the verdict's state and
// fixed description.
func (v verifier) statusStage(ctx, done context.Context, f *verifyFlight) (bool, error) {
	return f.stage(done, domain.StageStatus, func() (bool, error) {
		state, description, err := qaStatus(f.run.QA)
		if err != nil {
			return false, err
		}
		err = v.deps.Statuses.Publish(ctx, github.StatusRequest{
			Repository: f.run.Repository, SHA: f.run.HeadSHA, State: state,
			Context: qaContext, Description: description, TargetURL: f.pr.URL,
		})
		return err == nil, err
	})
}

// qaStatus is forja/qa's state and line for a verdict: success for one
// that bites, failure naming the inversion that escaped for one that does
// not, and pending for one that is not verifiable. Anything else publishes
// nothing.
func qaStatus(qa domain.QA) (state, description string, err error) {
	switch {
	case qa.Verdict == domain.VerdictBites:
		return github.StateSuccess, qaBitesDescription, nil
	case qa.Verdict == domain.VerdictDoesNotBite && qa.Escaped != "":
		return github.StateFailure, "Does not bite: inversion " + qa.Escaped + " passed every gate.", nil
	case qa.Verdict == domain.VerdictNotVerifiable && qa.Reason == domain.NotVerifiableOnlyExcludedPaths:
		return github.StatePending, qaOnlyExcludedDescription, nil
	case qa.Verdict == domain.VerdictNotVerifiable && qa.Reason == domain.NotVerifiableNoProductionCode:
		return github.StatePending, qaNoProductionDescription, nil
	}
	return "", "", fmt.Errorf("no forja/qa for verdict %q, reason %q", qa.Verdict, qa.Reason)
}

func evidenceOfVerify(f *verifyFlight) evidence.VerifyRun {
	return evidence.VerifyRun{
		RunID: f.run.ID, Issue: f.run.Issue, PullRequest: f.run.PullRequest, BaseBranch: f.co.Branch(),
		BaseSHA: f.run.BaseSHA, ContractSHA: f.run.ContractSHA, HeadSHA: f.run.HeadSHA,
		QA: f.run.QA, Reason: f.run.Reason,
		Baseline: f.run.Gates, Inversions: f.run.Inversions, Tokens: f.run.Tokens, Compute: f.run.Compute,
	}
}
