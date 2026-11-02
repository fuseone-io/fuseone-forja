package mcp

import (
	"context"
	"fmt"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/evidence"
	"github.com/fuseone-io/fuseone-forja/internal/github"
	"github.com/fuseone-io/fuseone-forja/internal/verify"
)

// qaContext is the commit status a verify run publishes on the head.
const qaContext = "forja/qa"

// The fixed descriptions of forja/qa for a diff that is not verifiable,
// one per reason.
const (
	qaOnlyExcludedDescription = "Not verifiable: only excluded paths changed; a person decides the merge."
	qaNoProductionDescription = "Not verifiable: no production code changed; a person decides the merge."
)

// verifyFlight is a verify run in progress: the run's flight and the pull
// request it verifies, read before the claim.
type verifyFlight struct {
	*flight
	pr domain.PullRequestDiff
}

// verifyStep is one stage of a verify run after the clone.
type verifyStep func(ctx, done context.Context, f *verifyFlight) (bool, error)

// work clones the base and reads its contract, then classifies the diff
// and publishes forja/qa under one deadline: the contract's wall clock
// counted from the claim. The pull request's head is never checked out.
func (v verifier) work(ctx, done context.Context, f *verifyFlight) (bool, error) {
	defer f.closeCheckout()
	if ok, err := v.cloneStage(ctx, done, f); !ok || err != nil {
		return false, err
	}
	runCtx, cancel := context.WithTimeout(ctx, f.left)
	defer cancel()
	for _, step := range []verifyStep{v.diffStage, v.statusStage} {
		if ok, err := step(runCtx, done, f); !ok || err != nil {
			return false, err
		}
	}
	return true, nil
}

// cloneStage clones the pull request's base branch and reads the contract
// at that commit, then sizes what is left of the wall clock.
func (v verifier) cloneStage(ctx, done context.Context, f *verifyFlight) (bool, error) {
	return f.stage(done, domain.StageClone, func() (bool, error) {
		co, err := v.deps.Cloner.Clone(ctx, f.run.Repository, f.base)
		if err != nil {
			return false, err
		}
		f.co, f.run.BaseSHA = co, co.SHA()
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

// diffStage classifies the changed paths under the base contract and
// renders the evidence. A diff with production code ends the run without
// an error: only an inversion can verify it.
func (v verifier) diffStage(_, done context.Context, f *verifyFlight) (bool, error) {
	return f.stage(done, domain.StageDiff, func() (bool, error) {
		outcome := verify.Classify(f.contract, f.pr.Files)
		f.run.QA = outcome.QA
		if outcome.Production {
			f.run.Reason = domain.ReasonInversionUnavailable
		}
		f.evidence = evidence.RenderVerify(evidenceOfVerify(f))
		return !outcome.Production, nil
	})
}

// statusStage publishes forja/qa pending on the head, never success: a
// diff that is not verifiable leaves the merge to a person.
func (v verifier) statusStage(ctx, done context.Context, f *verifyFlight) (bool, error) {
	return f.stage(done, domain.StageStatus, func() (bool, error) {
		description, err := qaDescription(f.run.QA)
		if err != nil {
			return false, err
		}
		err = v.deps.Statuses.Publish(ctx, github.StatusRequest{
			Repository: f.run.Repository, SHA: f.run.HeadSHA, State: github.StatePending,
			Context: qaContext, Description: description, TargetURL: f.pr.URL,
		})
		return err == nil, err
	})
}

// qaDescription is forja/qa's line for a not-verifiable verdict; any other
// verdict publishes nothing.
func qaDescription(qa domain.QA) (string, error) {
	if qa.Verdict == domain.VerdictNotVerifiable {
		switch qa.Reason {
		case domain.NotVerifiableOnlyExcludedPaths:
			return qaOnlyExcludedDescription, nil
		case domain.NotVerifiableNoProductionCode:
			return qaNoProductionDescription, nil
		}
	}
	return "", fmt.Errorf("no forja/qa for verdict %q, reason %q", qa.Verdict, qa.Reason)
}

func evidenceOfVerify(f *verifyFlight) evidence.VerifyRun {
	return evidence.VerifyRun{
		RunID: f.run.ID, Issue: f.run.Issue, PullRequest: f.run.PullRequest, BaseBranch: f.co.Branch(),
		BaseSHA: f.run.BaseSHA, ContractSHA: f.run.ContractSHA, HeadSHA: f.run.HeadSHA,
		QA: f.run.QA, Reason: f.run.Reason,
	}
}
