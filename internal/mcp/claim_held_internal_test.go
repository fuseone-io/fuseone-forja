package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

func strangerRuns() ownedRuns {
	return ownedRuns{runs: runstore.NewMemory(), owner: "client-a"}
}

// The refusal without the holder is still a held claim to errors.Is, as
// the one naming it is, and no longer carries the domain error that names
// the holder.
func TestHeldFor_holderNotTheCallers_stillErrClaimHeld(t *testing.T) {
	t.Parallel()
	held := &domain.ClaimHeldError{Holder: domain.Claim{RunID: "run-x", Repository: "acme/api", Issue: 3, ExpiresAt: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)}}
	if err := heldFor(context.Background(), strangerRuns(), held); !errors.Is(err, domain.ErrClaimHeld) || errors.As(err, new(*domain.ClaimHeldError)) {
		t.Fatalf("heldFor(unknown holder) = %#v, want ErrClaimHeld without the domain error that names the holder", err)
	}
}

// An error that is no held claim passes unchanged.
func TestHeldFor_notAHeldClaim_unchanged(t *testing.T) {
	t.Parallel()
	other := errors.New("contract unreadable")
	if err := heldFor(context.Background(), strangerRuns(), other); !errors.Is(err, other) || err.Error() != other.Error() {
		t.Fatalf("heldFor(%v) = %v, want it unchanged", other, err)
	}
}
