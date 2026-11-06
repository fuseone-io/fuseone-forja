// Package claim reserves an issue for one run: a ref under
// refs/forja/claims/<n> in the repository, pointing at a commit that
// records the run and its lease. The repository's own rules make the
// reservation atomic: a ref is created only if absent and moved only
// forward, so two runs can never both believe they hold the same issue.
package claim

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/github"
)

// refs is what the store needs from the repository: the ref calls and the
// one-file commits that carry a claim.
type refs interface {
	GetRef(ctx context.Context, repo, ref string) (github.RefObject, error)
	CreateRef(ctx context.Context, repo, ref, sha string) error
	UpdateRef(ctx context.Context, repo, ref, sha string) error
	DeleteRef(ctx context.Context, repo, ref string) error
	CommitFile(ctx context.Context, repo string, file github.FileCommit) (string, error)
	ReadFile(ctx context.Context, repo, sha, path string) ([]byte, error)
}

// Store claims and releases issues. A nil Journal means no crash
// recovery: a claim left by a crashed process then waits for its lease.
type Store struct {
	Refs    refs
	Now     func() time.Time
	Journal *Journal
}

// NewStore builds a store over the repository, the clock and the journal.
func NewStore(r refs, now func() time.Time, journal *Journal) *Store {
	return &Store{Refs: r, Now: now, Journal: journal}
}

// Claim reserves the issue for the run. A held, unexpired claim is refused
// with a *domain.ClaimHeldError naming the holder; an expired one is taken
// over and cited in the new claim; one that cannot be read is refused with
// domain.ErrClaimUnreadable and never taken over.
func (s *Store) Claim(ctx context.Context, req domain.ClaimRequest) (domain.Claim, error) {
	if err := req.Validate(); err != nil {
		return domain.Claim{}, fmt.Errorf("claim: %w", err)
	}
	if s.Refs == nil || s.Now == nil {
		return domain.Claim{}, errors.New("claim: Store needs Refs and Now")
	}
	now := s.Now().UTC().Truncate(time.Second)
	c := domain.Claim{
		RunID: req.RunID, Repository: req.Repository, Issue: req.Issue,
		CreatedAt: now, ExpiresAt: now.Add(req.Lease),
	}
	sha, err := s.commit(ctx, c, "")
	if err != nil {
		return domain.Claim{}, err
	}
	mctx, cancel, err := mutation(ctx)
	if err != nil {
		return domain.Claim{}, fmt.Errorf("claim issue %d of %s: %w", c.Issue, c.Repository, err)
	}
	defer cancel()
	err = s.Refs.CreateRef(mctx, c.Repository, domain.ClaimRef(c.Issue), sha)
	switch {
	case err == nil:
		return s.hold(mctx, c, sha)
	case errors.Is(err, github.ErrRefExists):
		return s.takeOver(ctx, c)
	}
	return domain.Claim{}, fmt.Errorf("claim issue %d of %s: %w", c.Issue, c.Repository, err)
}

// takeOver handles a ref that already exists: refuse while the lease
// runs, otherwise move the ref forward onto a claim that cites the old
// one. A move the repository refuses means another run got there first.
func (s *Store) takeOver(ctx context.Context, c domain.Claim) (domain.Claim, error) {
	holder, err := s.holder(ctx, c.Repository, c.Issue)
	if err != nil {
		return domain.Claim{}, err
	}
	if !holder.Expired(c.CreatedAt) {
		return domain.Claim{}, &domain.ClaimHeldError{Holder: holder}
	}
	c.Supersedes = &domain.ClaimSummary{RunID: holder.RunID, SHA: holder.SHA, ExpiresAt: holder.ExpiresAt}
	sha, err := s.commit(ctx, c, holder.SHA)
	if err != nil {
		return domain.Claim{}, err
	}
	mctx, cancel, err := mutation(ctx)
	if err != nil {
		return domain.Claim{}, fmt.Errorf("take over issue %d of %s: %w", c.Issue, c.Repository, err)
	}
	defer cancel()
	err = s.Refs.UpdateRef(mctx, c.Repository, domain.ClaimRef(c.Issue), sha)
	switch {
	case err == nil:
		return s.hold(mctx, c, sha)
	case errors.Is(err, github.ErrNotFastForward):
		winner, err := s.holder(ctx, c.Repository, c.Issue)
		if err != nil {
			return domain.Claim{}, err
		}
		return domain.Claim{}, &domain.ClaimHeldError{Holder: winner}
	}
	return domain.Claim{}, fmt.Errorf("take over issue %d of %s: %w", c.Issue, c.Repository, err)
}

// mutationTimeout bounds a ref mutation and what completes it, counted
// from its start, once the caller may be gone.
const mutationTimeout = 30 * time.Second

// mutation is the context a ref mutation and its journaling or giving
// back run on: detached from the caller's, since a mutation GitHub
// applied while the caller left must still be journaled or undone, and
// bounded by mutationTimeout. A caller already gone starts none.
func mutation(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mutationTimeout)
	return mctx, cancel, nil
}

// hold completes a claim the ref now points at: it is journaled so a crash
// can be recovered. A claim that cannot be journaled is given back, since
// nothing would release it after a crash. ctx is the mutation's.
func (s *Store) hold(ctx context.Context, c domain.Claim, sha string) (domain.Claim, error) {
	c.SHA = sha
	if s.Journal == nil {
		return c, nil
	}
	err := s.Journal.Put(Entry{Repository: c.Repository, Issue: c.Issue, RunID: c.RunID, SHA: c.SHA})
	if err == nil {
		return c, nil
	}
	if rerr := s.Refs.DeleteRef(ctx, c.Repository, domain.ClaimRef(c.Issue)); rerr != nil {
		return domain.Claim{}, fmt.Errorf("claim issue %d of %s: %w (and the ref could not be released: %w)", c.Issue, c.Repository, err, rerr)
	}
	return domain.Claim{}, fmt.Errorf("claim issue %d of %s: %w", c.Issue, c.Repository, err)
}

// holder reads the claim the ref points at.
func (s *Store) holder(ctx context.Context, repo string, issue int) (domain.Claim, error) {
	obj, err := s.Refs.GetRef(ctx, repo, domain.ClaimRef(issue))
	if err != nil {
		return domain.Claim{}, fmt.Errorf("read holder of issue %d of %s: %w", issue, repo, err)
	}
	body, err := s.Refs.ReadFile(ctx, repo, obj.SHA, claimPath)
	if err != nil {
		return domain.Claim{}, fmt.Errorf("read holder of issue %d of %s at %s: %w", issue, repo, obj.SHA, err)
	}
	holder, err := decodeClaim(body, repo, issue)
	if err != nil {
		return domain.Claim{}, fmt.Errorf("read holder of issue %d of %s at %s: %w: %w", issue, repo, obj.SHA, domain.ErrClaimUnreadable, err)
	}
	holder.SHA = obj.SHA
	return holder, nil
}

func (s *Store) commit(ctx context.Context, c domain.Claim, parent string) (string, error) {
	body, err := encodeClaim(c)
	if err != nil {
		return "", fmt.Errorf("claim issue %d of %s: %w", c.Issue, c.Repository, err)
	}
	sha, err := s.Refs.CommitFile(ctx, c.Repository, github.FileCommit{
		Path: claimPath, Body: body, Parent: parent,
		Message: fmt.Sprintf("forja: claim issue %d for run %s", c.Issue, c.RunID),
	})
	if err != nil {
		return "", fmt.Errorf("claim issue %d of %s: %w", c.Issue, c.Repository, err)
	}
	return sha, nil
}

// Release removes the ref if it still points at this claim. A ref that
// moved on or vanished wraps domain.ErrClaimLost and is left alone: it is
// another run's now. The journal forgets the claim in both cases; a
// release that could not verify the ref keeps it for recovery.
func (s *Store) Release(ctx context.Context, c domain.Claim) error {
	ref := domain.ClaimRef(c.Issue)
	obj, err := s.Refs.GetRef(ctx, c.Repository, ref)
	switch {
	case errors.Is(err, github.ErrRefMissing):
		return s.forget(c, fmt.Errorf("release issue %d of %s: %w: the ref is gone", c.Issue, c.Repository, domain.ErrClaimLost))
	case err != nil:
		return fmt.Errorf("release issue %d of %s: %w", c.Issue, c.Repository, err)
	case obj.SHA != c.SHA:
		return s.forget(c, fmt.Errorf("release issue %d of %s: %w: the ref points at %s, not %s", c.Issue, c.Repository, domain.ErrClaimLost, obj.SHA, c.SHA))
	}
	err = s.Refs.DeleteRef(ctx, c.Repository, ref)
	switch {
	case errors.Is(err, github.ErrRefMissing):
		return s.forget(c, fmt.Errorf("release issue %d of %s: %w: the ref is gone", c.Issue, c.Repository, domain.ErrClaimLost))
	case err != nil:
		return fmt.Errorf("release issue %d of %s: %w", c.Issue, c.Repository, err)
	}
	return s.forget(c, nil)
}

// forget drops the journal entry and returns err joined with any journal
// failure, so the caller still sees the outcome of the release.
func (s *Store) forget(c domain.Claim, err error) error {
	if s.Journal == nil {
		return err
	}
	if jerr := s.Journal.Remove(c.Repository, c.Issue); jerr != nil {
		return errors.Join(err, fmt.Errorf("release issue %d of %s: %w", c.Issue, c.Repository, jerr))
	}
	return err
}
