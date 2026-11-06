package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

// The refusal without the holder is still a held claim to errors.Is, as
// the one naming it is; an error that is no held claim passes unchanged.
func TestHeldFor_holderNotTheCallers_stillErrClaimHeld(t *testing.T) {
	t.Parallel()
	runs := ownedRuns{runs: runstore.NewMemory(), owner: "client-a"}
	held := &domain.ClaimHeldError{Holder: domain.Claim{RunID: "run-x", Repository: "acme/api", Issue: 3, ExpiresAt: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)}}
	if err := heldFor(context.Background(), runs, held); !errors.Is(err, domain.ErrClaimHeld) || errors.As(err, new(*domain.ClaimHeldError)) {
		t.Fatalf("heldFor(unknown holder) = %#v, want ErrClaimHeld without the domain error that names the holder", err)
	}
	other := errors.New("contract unreadable")
	if err := heldFor(context.Background(), runs, other); !errors.Is(err, other) || err.Error() != other.Error() {
		t.Fatalf("heldFor(%v) = %v, want it unchanged", other, err)
	}
}
