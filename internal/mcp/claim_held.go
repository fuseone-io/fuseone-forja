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

// heldFor is err as client may read it. A claim refused because it is held
// names its holder only when runs records that run under client; a holder
// of another client, one runs does not know and one runs could not look up
// are all the same refusal without the holder. Any other err is unchanged.
func heldFor(ctx context.Context, runs RunReader, client string, err error) error {
	var held *domain.ClaimHeldError
	if !errors.As(err, &held) {
		return err
	}
	if _, getErr := (ownedRuns{runs: runs, owner: client}).Get(ctx, held.Holder.RunID); getErr == nil {
		return err
	}
	return claimHeldElsewhere{repo: held.Holder.Repository, issue: held.Holder.Issue, expires: held.Holder.ExpiresAt}
}
