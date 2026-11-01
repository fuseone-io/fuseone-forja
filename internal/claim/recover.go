package claim

import (
	"context"
	"errors"
	"fmt"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/github"
)

// Recover releases every journaled claim whose ref still points at it: the
// claims a previous process held when it died. A ref that moved on or
// vanished is another run's and is left alone. Every entry is forgotten
// once looked at; a repository that cannot be reached stops the recovery
// with the entry kept. It returns the claims it released.
func (s *Store) Recover(ctx context.Context) ([]domain.Claim, error) {
	if s.Journal == nil {
		return nil, nil
	}
	if s.Refs == nil {
		return nil, errors.New("recover claims: Store needs Refs")
	}
	entries, err := s.Journal.List()
	if err != nil {
		return nil, fmt.Errorf("recover claims: %w", err)
	}
	var released []domain.Claim
	for _, e := range entries {
		ours, err := s.recoverEntry(ctx, e)
		if err != nil {
			return released, err
		}
		if ours {
			released = append(released, domain.Claim{RunID: e.RunID, Repository: e.Repository, Issue: e.Issue, SHA: e.SHA})
		}
	}
	return released, nil
}

// recoverEntry releases one journaled claim when the ref is still ours and
// reports whether it was.
func (s *Store) recoverEntry(ctx context.Context, e Entry) (bool, error) {
	ref := domain.ClaimRef(e.Issue)
	obj, err := s.Refs.GetRef(ctx, e.Repository, ref)
	switch {
	case errors.Is(err, github.ErrRefMissing):
		return false, s.forgetEntry(e)
	case err != nil:
		return false, fmt.Errorf("recover issue %d of %s: %w", e.Issue, e.Repository, err)
	case obj.SHA != e.SHA:
		return false, s.forgetEntry(e)
	}
	err = s.Refs.DeleteRef(ctx, e.Repository, ref)
	if err != nil && !errors.Is(err, github.ErrRefMissing) {
		return false, fmt.Errorf("recover issue %d of %s: %w", e.Issue, e.Repository, err)
	}
	return true, s.forgetEntry(e)
}

func (s *Store) forgetEntry(e Entry) error {
	if err := s.Journal.Remove(e.Repository, e.Issue); err != nil {
		return fmt.Errorf("recover issue %d of %s: %w", e.Issue, e.Repository, err)
	}
	return nil
}
