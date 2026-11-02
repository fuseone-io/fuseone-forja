package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/fuseone-io/fuseone-forja/internal/agent"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/verify"
)

// QAAgent is what forja.verify needs to run the QA agent on the pull
// request's head: the agent's outcome, which candidates it exported, and
// what the proxies recorded.
type QAAgent interface {
	RunQA(ctx context.Context, t agent.QATask) (agent.QAResult, error)
}

const (
	// qaBranchPrefix names the candidate branches, forja-qa/1 onwards, in
	// the QA agent's order of preference.
	qaBranchPrefix = "forja-qa/"
	// qaCandidates is how many candidates the QA agent is offered.
	qaCandidates = 4
	// qaInversions is how many admitted candidates run, the first in order.
	qaInversions = 2
)

// inversions verifies a diff with production code: the base contract's
// gates on the head, the QA agent's candidates, the gates on each admitted
// one, and the verdict they fold to. A diff that is not verifiable goes
// straight on.
func (v verifier) inversions(ctx, done context.Context, f *verifyFlight) (bool, error) {
	if !f.production {
		return true, nil
	}
	dir, err := os.MkdirTemp("", "forja-qa-*")
	if err != nil {
		return false, fmt.Errorf("bundle directory: %w", err)
	}
	// The bundles were imported or refused by now; a directory that could
	// not be removed changes neither.
	defer func() { _ = os.RemoveAll(dir) }()
	f.candidates = candidates(dir)
	for _, step := range []verifyStep{v.baselineStage, v.qaStage, v.inversionStages} {
		if ok, err := step(ctx, done, f); !ok || err != nil {
			return false, err
		}
	}
	return fold(f), nil
}

// candidates are the branches the QA agent is offered, each with its
// bundle in dir.
func candidates(dir string) []agent.Candidate {
	out := make([]agent.Candidate, 0, qaCandidates)
	for k := 1; k <= qaCandidates; k++ {
		out = append(out, agent.Candidate{
			Branch: qaBranchPrefix + strconv.Itoa(k), Bundle: filepath.Join(dir, "candidate-"+strconv.Itoa(k)+".bundle"),
		})
	}
	return out
}

// inversionStages runs an inversion stage for every admitted candidate, in
// order, with no early stop.
func (v verifier) inversionStages(ctx, done context.Context, f *verifyFlight) (bool, error) {
	for i := range f.run.Inversions {
		if !f.run.Inversions[i].Admitted {
			continue
		}
		if ok, err := v.inversionStage(ctx, done, f, i); !ok || err != nil {
			return false, err
		}
	}
	return true, nil
}

// baselineStage runs the base contract's gates on the head. A head that
// is not green fails the run without an error: an inversion of a red head
// proves nothing.
func (v verifier) baselineStage(ctx, done context.Context, f *verifyFlight) (bool, error) {
	return f.stage(done, domain.StageGates, func() (bool, error) {
		report, err := v.deps.Gates.Run(ctx, f.run.ID, f.contract, f.co.Dir())
		f.charge(report.Compute, domain.TokenUsage{})
		if err != nil {
			return false, err
		}
		f.run.Gates, f.run.Registry = report, report.Registry
		f.run.Egress = f.run.Egress.Merge(report.Egress)
		if !report.OK() {
			f.run.Reason = domain.ReasonGateFailed
			return false, nil
		}
		return true, nil
	})
}

// qaStage runs the QA agent on the head and admits its candidates. The cap
// is the reason whatever else happened; an agent stopped or failed ends
// the run without an error.
func (v verifier) qaStage(ctx, done context.Context, f *verifyFlight) (bool, error) {
	return f.stage(done, domain.StageAgent, func() (bool, error) {
		issue, err := v.deps.Issues.Read(ctx, f.run.Repository, f.run.Issue)
		if err != nil {
			return false, err
		}
		res, err := v.deps.QA.RunQA(ctx, agent.QATask{
			RunID: f.run.ID, Contract: f.contract, Tree: f.co.Dir(), Issue: issue, PullRequestBody: f.pr.Body,
			BaseSHA: f.run.BaseSHA, HeadSHA: f.run.HeadSHA, Production: verify.ProductionPaths(f.contract, f.pr.Files),
			Candidates: f.candidates,
		})
		f.charge(res.Compute, res.Tokens)
		f.run.Egress = f.run.Egress.Merge(res.Egress)
		if res.Capped {
			f.run.Reason = domain.ReasonTokenCap
		}
		switch {
		case err != nil:
			return false, err
		case res.Capped || res.TimedOut || res.ExitCode != 0:
			return false, nil
		}
		return v.admit(ctx, f, res.Exported)
	})
}

// admit looks at the exported candidates in order until qaInversions are
// admitted, keeping each it looked at in the run. None admitted fails the
// run without an error.
func (v verifier) admit(ctx context.Context, f *verifyFlight, exported []bool) (bool, error) {
	admitted := 0
	for i, c := range f.candidates {
		if admitted == qaInversions {
			break
		}
		if i >= len(exported) || !exported[i] {
			continue
		}
		inv, err := v.candidate(ctx, f, c)
		if err != nil {
			return false, err
		}
		f.run.Inversions = append(f.run.Inversions, inv)
		if inv.Admitted {
			admitted++
		}
	}
	if admitted == 0 {
		f.run.Reason = domain.ReasonInversionUnavailable
		return false, nil
	}
	return true, nil
}

// candidate imports one candidate on the control plane's clone, reads what
// it changes against the head and against the base, and decides its
// admission under the base contract.
func (v verifier) candidate(ctx context.Context, f *verifyFlight, c agent.Candidate) (domain.Inversion, error) {
	inv := domain.Inversion{Branch: c.Branch}
	commit, err := f.co.Adopt(ctx, c.Bundle, c.Branch)
	if errors.Is(err, domain.ErrNoCommits) {
		inv.Rejected = domain.RejectEmpty
		return inv, nil
	}
	if err != nil {
		return inv, err
	}
	change, err := f.co.Changes(ctx, f.run.HeadSHA, commit)
	if err != nil {
		return inv, err
	}
	fromBase, err := f.co.Changes(ctx, f.run.BaseSHA, commit)
	if err != nil {
		return inv, err
	}
	inv.Commit, inv.Paths, inv.Patch, inv.PatchTruncated = commit, change.Paths, change.Patch, change.Truncated
	inv.Admitted, inv.Rejected = verify.Admit(f.contract, f.pr.Files, change, fromBase)
	return inv, nil
}

// inversionStage checks the admitted candidate i out in the clone and runs
// the base contract's gates on it. What the gates showed is the
// inversion's outcome, never the stage's: every admitted one runs.
func (v verifier) inversionStage(ctx, done context.Context, f *verifyFlight, i int) (bool, error) {
	return f.stage(done, domain.StageInversion, func() (bool, error) {
		inv := &f.run.Inversions[i]
		commit, err := f.co.Adopt(ctx, f.bundleOf(inv.Branch), inv.Branch)
		if err != nil {
			return false, err
		}
		if commit != inv.Commit {
			return false, fmt.Errorf("%s: adopted %s, admitted %s", inv.Branch, commit, inv.Commit)
		}
		report, err := v.deps.Gates.Run(ctx, f.run.ID, f.contract, f.co.Dir())
		f.charge(report.Compute, domain.TokenUsage{})
		if err != nil {
			return false, err
		}
		f.run.Egress, f.run.Registry = f.run.Egress.Merge(report.Egress), f.run.Registry.Merge(report.Registry)
		inv.Gates = report
		inv.Outcome, _ = verify.Judge(report)
		return true, nil
	})
}

// bundleOf is where branch's commits were exported.
func (f *verifyFlight) bundleOf(branch string) string {
	for _, c := range f.candidates {
		if c.Branch == branch {
			return c.Bundle
		}
	}
	return ""
}

// fold sets the verdict of the inversions that ran. One whose gates showed
// neither red at a gate nor every step passed fails the run without an
// error.
func fold(f *verifyFlight) bool {
	var ran []domain.Inversion
	for _, inv := range f.run.Inversions {
		if inv.Admitted {
			ran = append(ran, inv)
		}
	}
	verdict, escaped, ok := verify.Fold(ran)
	if !ok {
		f.run.Reason = domain.ReasonInversionInconclusive
		return false
	}
	f.run.QA.Verdict, f.run.QA.Escaped = verdict, escaped
	return true
}
