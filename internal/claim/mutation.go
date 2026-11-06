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

// SettleBound is the longest Claim keeps working once its caller left on
// a path that answers a claim, which the run then holds: the ref mutation,
// then the read-back that settles an answer it lost, each on its own
// budget. A claim that is refused instead may also give the ref back on a
// third budget; no run follows it.
const SettleBound = mutationTimeout + followUpTimeout

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

// unsettled decides a mutation that answered an error other than the
// refusals Claim and takeOver read. A 4xx was not applied: cause is the
// answer. Any other error, a lost answer or a request failed on the way,
// leaves the outcome open, and the ref is read back: at the claim's own
// commit the mutation applied, and the claim is held. Otherwise GitHub may
// still apply it later, so the claim's commit is journaled: recovery
// releases the ref if it points there, and forgets the entry if not.
func (s *Store) unsettled(ctx context.Context, c domain.Claim, sha string, cause error) (domain.Claim, error) {
	if refused(cause) {
		return domain.Claim{}, cause
	}
	rctx, cancel := s.followUp(ctx)
	defer cancel()
	obj, err := s.Refs.GetRef(rctx, c.Repository, domain.ClaimRef(c.Issue))
	if err == nil && obj.SHA == sha {
		return s.hold(ctx, c, sha)
	}
	if err != nil && !errors.Is(err, github.ErrRefMissing) {
		cause = fmt.Errorf("%w (and the ref could not be read back: %w)", cause, err)
	}
	c.SHA = sha
	return domain.Claim{}, s.journalOpen(c, cause)
}

// refused says GitHub answered the request with a 4xx, so it applied
// nothing.
func refused(err error) bool {
	var se *github.StatusError
	return errors.As(err, &se) && se.Status >= 400 && se.Status < 500
}

// journalOpen records a claim whose mutation may yet apply and returns
// cause, with any journal failure joined.
func (s *Store) journalOpen(c domain.Claim, cause error) error {
	if s.Journal == nil {
		return cause
	}
	if err := s.Journal.Put(entryOf(c)); err != nil {
		return errors.Join(cause, fmt.Errorf("claim issue %d of %s: %w", c.Issue, c.Repository, err))
	}
	return cause
}

func entryOf(c domain.Claim) Entry {
	return Entry{Repository: c.Repository, Issue: c.Issue, RunID: c.RunID, SHA: c.SHA}
}
