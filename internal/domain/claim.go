package domain

import (
	"errors"
	"fmt"
	"time"
)

// ClaimRefPrefix is the namespace of the issue reservations in a repository.
const ClaimRefPrefix = "refs/forja/claims/"

// Lease defaults. A lease outlives the run's wall clock so that a run still
// working is never taken over; a contract without limits gets a flat hour.
const (
	DefaultLeaseMargin = 15 * time.Minute
	DefaultLease       = time.Hour
)

// ErrClaimHeld means the issue is reserved by a run whose lease has not
// expired. The error that wraps it is a *ClaimHeldError naming that run.
var ErrClaimHeld = errors.New("claim held")

// ErrClaimLost means a release found the ref pointing at another run's
// claim: the lease expired and someone took it over.
var ErrClaimLost = errors.New("claim lost")

// ErrClaimUnreadable means the ref exists but its claim cannot be decoded.
// Such a claim is refused, never taken over.
var ErrClaimUnreadable = errors.New("claim unreadable")

// ClaimRef is the ref that reserves an issue.
func ClaimRef(issue int) string {
	return fmt.Sprintf("%s%d", ClaimRefPrefix, issue)
}

// ClaimSummary identifies the claim a run took over.
type ClaimSummary struct {
	RunID     string
	SHA       string
	ExpiresAt time.Time
}

// Claim is a reservation held in a repository: the ref points at SHA, a
// commit carrying the claim's record.
type Claim struct {
	RunID      string
	Repository string
	Issue      int
	SHA        string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	// Supersedes is the expired claim this one took over; nil otherwise.
	Supersedes *ClaimSummary
}

// Expired reports whether the lease is over at now.
func (c Claim) Expired(now time.Time) bool {
	return !now.Before(c.ExpiresAt)
}

// ClaimRequest is what a run asks for: the issue, under its own id, for the
// length of the lease.
type ClaimRequest struct {
	Repository string
	Issue      int
	RunID      string
	Lease      time.Duration
}

// Validate rejects a request that could not reserve anything.
func (r ClaimRequest) Validate() error {
	switch {
	case r.Repository == "":
		return errors.New("claim request has no repository")
	case r.Issue <= 0:
		return fmt.Errorf("claim request issue %d is not positive", r.Issue)
	case r.RunID == "":
		return errors.New("claim request has no run id")
	case r.Lease <= 0:
		return fmt.Errorf("claim request lease %v is not positive", r.Lease)
	}
	return nil
}

// LeaseFor sizes the lease from the contract's limits. A configured lease
// wins but must outlive the wall clock; otherwise the lease is the wall
// clock plus DefaultLeaseMargin, or DefaultLease when the contract sets no
// wall clock.
func LeaseFor(limits Limits, configured time.Duration) (time.Duration, error) {
	if configured > 0 {
		if configured <= limits.WallClock {
			return 0, fmt.Errorf("configured lease %v does not exceed limits.wall_clock %v", configured, limits.WallClock)
		}
		return configured, nil
	}
	if limits.WallClock == 0 {
		return DefaultLease, nil
	}
	return limits.WallClock + DefaultLeaseMargin, nil
}

// ClaimHeldError is the refusal of a claim: the holder is named so the
// caller can find the run that owns the issue.
type ClaimHeldError struct {
	Holder Claim
}

func (e *ClaimHeldError) Error() string {
	return fmt.Sprintf("issue %d of %s is claimed by run %s until %s",
		e.Holder.Issue, e.Holder.Repository, e.Holder.RunID, e.Holder.ExpiresAt.UTC().Format(time.RFC3339))
}

// Is makes errors.Is(err, ErrClaimHeld) true for every held claim.
func (e *ClaimHeldError) Is(target error) bool { return target == ErrClaimHeld }
