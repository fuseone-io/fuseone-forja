package workspace

import (
	"reflect"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/proxy/registry"
)

// The operator holds credentials for many areas; a run's proxy receives
// those of the entries its contract declares, and no other area's.
func TestCredentialsFor_operatorHoldsMore_onlyDeclaredPicked(t *testing.T) {
	t.Parallel()
	declared := registry.Credential{Username: "reader-a", Password: "secret-a"}
	all := map[string]registry.Credential{
		"ghcr.io/org-a": declared,
		"ghcr.io/org-b": {Username: "reader-b", Password: "secret-b"},
		"ghcr.io":       {Username: "reader-host", Password: "secret-host"},
	}

	got, err := credentialsFor(all, []string{"ghcr.io/org-a"})
	if err != nil {
		t.Fatalf("credentialsFor: %v", err)
	}
	if want := map[string]registry.Credential{"ghcr.io/org-a": declared}; !reflect.DeepEqual(got, want) {
		t.Fatalf("picked %d credentials for %v, want only ghcr.io/org-a's", len(got), keys(got))
	}
}

func keys(m map[string]registry.Credential) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
