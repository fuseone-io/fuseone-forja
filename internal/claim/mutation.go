package claim

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/github"
)

const (
	// mutationTimeout bounds a ref mutation, counted from its start, once
	// the caller may be gone.
	mutationTimeout = 30 * time.Second
	// followUpTimeout bounds each call that settles a mutation after it:
	// reading the ref back, giving it back. It is its own budget, so a
	// mutation that spent all of its own still gets settled.
	followUpTimeout = 30 * time.Second
)

// mutation is the context a ref mutation runs on: detached from the
// caller's, since a mutation GitHub applied while the caller left must
// still be journaled or undone, and bounded by the mutation budget. A
// caller already gone starts none.
func (s *Store) mutation(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget(s.mutationTimeout, mutationTimeout))
	return mctx, cancel, nil
}

// followUp is a detached context with a fresh follow-up budget.
func (s *Store) followUp(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), budget(s.followUpTimeout, followUpTimeout))
}

func budget(set, fallback time.Duration) time.Duration {
	if set > 0 {
		return set
	}
	return fallback
}

// hold completes a claim the ref now points at: it is journaled so a crash
// can be recovered. A claim that cannot be journaled is given back on a
// follow-up budget of its own, since nothing would release it after a
// crash and the mutation may have spent all of ctx's.
func (s *Store) hold(ctx context.Context, c domain.Claim, sha string) (domain.Claim, error) {
	c.SHA = sha
	if s.Journal == nil {
		return c, nil
	}
	err := s.Journal.Put(entryOf(c))
	if err == nil {
		return c, nil
	}
	gctx, cancel := s.followUp(ctx)
	defer cancel()
	if rerr := s.Refs.DeleteRef(gctx, c.Repository, domain.ClaimRef(c.Issue)); rerr != nil {
		return domain.Claim{}, fmt.Errorf("claim issue %d of %s: %w (and the ref could not be released: %w)", c.Issue, c.Repository, err, rerr)
	}
	return domain.Claim{}, fmt.Errorf("claim issue %d of %s: %w", c.Issue, c.Repository, err)
}

// unsettled decides a mutation that answered an error GitHub may have
// applied anyway, such as a lost answer. The ref is read back: pointing at
// the claim's own commit, the mutation applied and the claim is held; not
// pointing at it, cause is the answer. A ref that cannot be read back is
// journaled under the claim's commit, so recovery releases it if the
// mutation did apply and forgets it otherwise.
func (s *Store) unsettled(ctx context.Context, c domain.Claim, sha string, cause error) (domain.Claim, error) {
	rctx, cancel := s.followUp(ctx)
	defer cancel()
	obj, err := s.Refs.GetRef(rctx, c.Repository, domain.ClaimRef(c.Issue))
	switch {
	case err == nil && obj.SHA == sha:
		return s.hold(ctx, c, sha)
	case err == nil, errors.Is(err, github.ErrRefMissing):
		return domain.Claim{}, cause
	}
	cause = fmt.Errorf("%w (and the ref could not be read back: %w)", cause, err)
	if s.Journal == nil {
		return domain.Claim{}, cause
	}
	c.SHA = sha
	if jerr := s.Journal.Put(entryOf(c)); jerr != nil {
		return domain.Claim{}, errors.Join(cause, fmt.Errorf("claim issue %d of %s: %w", c.Issue, c.Repository, jerr))
	}
	return domain.Claim{}, cause
}

func entryOf(c domain.Claim) Entry {
	return Entry{Repository: c.Repository, Issue: c.Issue, RunID: c.RunID, SHA: c.SHA}
}
