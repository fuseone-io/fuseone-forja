package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// claimHeldElsewhere is a claim held by a run the caller does not own: it
// names the repository, the issue and the lease expiry, and neither the
// holder's run nor its client.
type claimHeldElsewhere struct {
	repo    string
	issue   int
	expires time.Time
}

func (e claimHeldElsewhere) Error() string {
	return fmt.Sprintf("issue %d of %s is claimed by another run until %s",
		e.issue, e.repo, e.expires.UTC().Format(time.RFC3339))
}

// Is makes errors.Is(err, domain.ErrClaimHeld) true for it as for the
// refusal it renders.
func (e claimHeldElsewhere) Is(target error) bool { return target == domain.ErrClaimHeld }

// heldFor is err as the owner of runs may read it. A claim refused because
// it is held names its holder only when the owner owns that run; any other
// holder is the same refusal without it. Any other err is unchanged.
func heldFor(ctx context.Context, runs ownedRuns, err error) error {
	var held *domain.ClaimHeldError
	if !errors.As(err, &held) || runs.owns(ctx, held.Holder.RunID) {
		return err
	}
	return claimHeldElsewhere{repo: held.Holder.Repository, issue: held.Holder.Issue, expires: held.Holder.ExpiresAt}
}
