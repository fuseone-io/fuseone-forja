package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/contract"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// lifecycle is what every run that holds an issue goes through: the lease
// sized from the contract, the claim, the records, the release on every
// outcome and the deadline of its work.
type lifecycle struct {
	deps Deps
}

// runTarget is what a run claims: the issue of the repository it works on,
// for which tool, from which base branch, and the client the run is
// recorded under.
type runTarget struct {
	tool   domain.Tool
	repo   string
	issue  int
	base   string
	client string
}

// runStep is one stage of a run's work, run under the run's deadline
// with its records on done.
type runStep func(ctx, done context.Context, f *flight) (bool, error)

// claim sizes the lease, holds the issue for a new run and returns the
// run's flight with its claim stage over. A refused claim is the error and
// records nothing.
func (l lifecycle) claim(ctx context.Context, t runTarget) (*flight, error) {
	t.client = clientOf(ctx).Name
	if t.client == "" {
		return nil, errNoClient
	}
	lease, err := l.lease(ctx, t.repo)
	if err != nil {
		return nil, err
	}
	started := l.deps.Now()
	c, err := l.deps.Claims.Claim(ctx, domain.ClaimRequest{
		Repository: t.repo, Issue: t.issue, RunID: l.deps.NewID(), Lease: lease,
	})
	if err != nil {
		return nil, heldFor(ctx, ownedRuns{runs: l.deps.Runs, owner: t.client}, err)
	}
	return newFlight(l.deps, t, c, claimStage(started, l.deps.Now())), nil
}

// lease reads the contract at the base commit and sizes the lease from its
// limits; a contract that does not parse reserves nothing.
func (l lifecycle) lease(ctx context.Context, repo string) (time.Duration, error) {
	src, err := l.deps.Contracts.Read(ctx, repo)
	if err != nil {
		return 0, err
	}
	parsed, err := contract.Parse(src.Body)
	if err != nil {
		return 0, fmt.Errorf("contract at %s: %w", src.SHA, err)
	}
	return domain.LeaseFor(parsed.Limits, l.deps.Lease)
}

// run records the run as running, does its work, releases the claim and
// records the terminal state, on every outcome. It is succeeded only when
// the work and the release worked. Any failure of the work or the release
// is returned after the run was recorded failed.
func (l lifecycle) run(ctx context.Context, f *flight, work runStep) error {
	// The caller may be gone by now. Once the issue is claimed, the records
	// and the release run on a context detached from the caller's, each
	// bounded from its own start, so neither the ref nor a run stuck in
	// running is left behind however long the work took.
	done := context.WithoutCancel(ctx)
	if err := f.put(done); err != nil {
		return errors.Join(err, l.release(done, f.claim))
	}
	delivered, workErr := work(ctx, done, f)
	releaseErr := l.releaseStage(done, f)
	finished := l.deps.Now()
	f.run.FinishedAt, f.run.State = &finished, domain.StateFailed
	if delivered && workErr == nil && releaseErr == nil {
		f.run.State, f.run.Reason = domain.StateSucceeded, ""
	}
	if err := f.put(done); err != nil {
		return errors.Join(workErr, releaseErr, err)
	}
	return errors.Join(workErr, releaseErr)
}

// releaseStage frees the issue as the release stage. The release happens
// even when its start could not be recorded; the terminal record ends it.
func (l lifecycle) releaseStage(done context.Context, f *flight) error {
	recordErr := f.begin(done, domain.StageRelease)
	err := l.release(done, f.claim)
	f.finish(err == nil)
	return errors.Join(err, recordErr)
}

func (l lifecycle) release(ctx context.Context, c domain.Claim) error {
	ctx, cancel := context.WithTimeout(ctx, FinishTimeout)
	defer cancel()
	if err := l.deps.Claims.Release(ctx, c); err != nil {
		return fmt.Errorf("release claim: %w", err)
	}
	return nil
}

// timeLeft is how long the run may work under the held claim: the
// contract's wall clock counted from the claim. The contract the run works
// under may differ from the one the lease was sized from, so it is refused
// when it asks for a longer lease than the claim holds, or when its wall
// clock would end at or after the lease, or is already spent.
func (l lifecycle) timeLeft(c domain.Claim, parsed domain.Contract) (time.Duration, error) {
	held := c.ExpiresAt.Sub(c.CreatedAt)
	need, err := domain.LeaseFor(parsed.Limits, l.deps.Lease)
	if err != nil {
		return 0, err
	}
	if need > held {
		return 0, fmt.Errorf("needs a lease of %v, the claim holds %v", need, held)
	}
	end := c.CreatedAt.Add(wallClock(parsed))
	if !end.Before(c.ExpiresAt) {
		return 0, fmt.Errorf("wall clock %v does not end before the claim's lease of %v", wallClock(parsed), held)
	}
	left := end.Sub(l.deps.Now())
	if left <= 0 {
		return 0, fmt.Errorf("wall clock %v spent before the work started, within the claim's lease of %v", wallClock(parsed), held)
	}
	return left, nil
}

// dependency is one collaborator a tool reaches, and whether it is wired.
type dependency struct {
	name string
	set  bool
}

// notWired names every dependency that is not set, or is nil when all are.
func notWired(required []dependency) error {
	var missing []string
	for _, r := range required {
		if !r.set {
			missing = append(missing, r.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("not wired: %s required", strings.Join(missing, ", "))
	}
	return nil
}
