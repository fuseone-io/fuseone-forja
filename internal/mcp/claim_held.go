package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// claimHeldUnnamed is a claim held by a run the caller is not shown: it
// names the repository, the issue and the lease expiry, and neither the
// holder's run nor its client. Its wording holds whoever the holder is,
// since a run the store cannot vouch for may be the caller's own.
type claimHeldUnnamed struct {
	repo    string
	issue   int
	expires time.Time
}

func (e claimHeldUnnamed) Error() string {
	return fmt.Sprintf("issue %d of %s is claimed by a run until %s",
		e.issue, e.repo, e.expires.UTC().Format(time.RFC3339))
}

// Is makes errors.Is(err, domain.ErrClaimHeld) true for it as for the
// refusal it renders.
func (e claimHeldUnnamed) Is(target error) bool { return target == domain.ErrClaimHeld }

// heldFor is err as the owner of runs may read it. A claim refused because
// it is held names its holder only when the owner owns that run; any other
// holder is the same refusal without it, which replaces err whole since
// whatever wraps the refusal may print the holder. Any other err is
// unchanged.
func heldFor(ctx context.Context, runs ownedRuns, err error) error {
	var held *domain.ClaimHeldError
	if !errors.As(err, &held) || runs.owns(ctx, held.Holder.RunID) {
		return err
	}
	return claimHeldUnnamed{repo: held.Holder.Repository, issue: held.Holder.Issue, expires: held.Holder.ExpiresAt}
}
