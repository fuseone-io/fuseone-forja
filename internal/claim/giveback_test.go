package claim_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/claim"
)

// The mutation succeeds having spent its whole budget and the journal then
// fails: the compensating delete runs on a budget of its own, so the ref
// comes off.
func TestStore_claim_mutationSpentBudgetJournalFails_refGivenBack(t *testing.T) {
	t.Parallel()
	for op := range mutations() {
		t.Run(op, func(t *testing.T) {
			t.Parallel()
			notADir := filepath.Join(t.TempDir(), "claims")
			if err := os.WriteFile(notADir, []byte("a file where the journal directory should be"), 0o600); err != nil {
				t.Fatal(err)
			}
			fake, store := newStalling(t, op, &claim.Journal{Dir: notADir})
			fake.stallOn, fake.lateOn = "", op
			if _, err := store.Claim(context.Background(), request("run-1")); err == nil {
				t.Fatal("Claim succeeded although the journal could not record it")
			}
			if sha, present := fake.refSHA(repo, ref); present {
				t.Fatalf("ref still points at %s: the give-back died with the mutation's budget", sha)
			}
		})
	}
}
