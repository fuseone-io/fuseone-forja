package domain_test

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"testing/quick"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

func TestClient_reaches_patternsAndMalformedRepositories(t *testing.T) {
	t.Parallel()
	explicit := domain.Client{Name: "pilot", Repositories: []string{"acme/payments"}}
	owner := domain.Client{Name: "staging", Repositories: []string{"acme/*"}}
	tests := []struct {
		name   string
		client domain.Client
		repo   string
		want   bool
	}{
		{name: "explicit, same", client: explicit, repo: "acme/payments", want: true},
		{name: "explicit, owner in another case", client: explicit, repo: "ACME/payments", want: true},
		{name: "explicit, name in another case", client: explicit, repo: "acme/Payments", want: true},
		{name: "explicit, other name", client: explicit, repo: "acme/ledger", want: false},
		{name: "explicit, name prefix", client: explicit, repo: "acme/pay", want: false},
		{name: "explicit, name extended", client: explicit, repo: "acme/payments2", want: false},
		{name: "explicit, other owner", client: explicit, repo: "evil/payments", want: false},
		{name: "explicit, wildcard asked", client: explicit, repo: "acme/*", want: false},
		{name: "owner, any name", client: owner, repo: "acme/ledger", want: true},
		{name: "owner, owner in another case", client: owner, repo: "Acme/ledger", want: true},
		{name: "owner, other owner", client: owner, repo: "acme-co/ledger", want: false},
		{name: "owner, owner prefix", client: owner, repo: "acm/ledger", want: false},
		{name: "owner, wildcard asked", client: owner, repo: "acme/*", want: false},
		{name: "owner, dot name", client: owner, repo: "acme/.", want: false},
		{name: "owner, dot-dot name", client: owner, repo: "acme/..", want: false},
		{name: "owner, dot-dot then a path", client: owner, repo: "acme/../evil/x", want: false},
		{name: "owner, a third segment", client: owner, repo: "acme/x/y", want: false},
		{name: "owner, empty name", client: owner, repo: "acme/", want: false},
		{name: "owner, no slash", client: owner, repo: "acme", want: false},
		{name: "owner, empty", client: owner, repo: "", want: false},
		{name: "owner, escaped dots", client: owner, repo: "acme/%2e%2e", want: false},
		{name: "owner, a space", client: owner, repo: "acme/pay ments", want: false},
		{name: "explicit, non-ASCII that folds to the name", client: domain.Client{Name: "k", Repositories: []string{"acme/key"}}, repo: "acme/Key", want: false},
		{name: "owner, non-ASCII that folds to the owner", client: domain.Client{Name: "k", Repositories: []string{"kacme/*"}}, repo: "Kacme/x", want: false},
		{name: "dot owner pattern reaches nothing", client: domain.Client{Name: "d", Repositories: []string{"../*"}}, repo: "../x", want: false},
		{name: "wildcard owner pattern reaches nothing", client: domain.Client{Name: "w", Repositories: []string{"*/*"}}, repo: "acme/x", want: false},
		{name: "second pattern", client: domain.Client{Name: "two", Repositories: []string{"acme/payments", "beta/*"}}, repo: "beta/x", want: true},
		{name: "zero client", client: domain.Client{}, repo: "acme/payments", want: false},
		{name: "unrestricted", client: domain.Client{Name: domain.StdioClientName, Unrestricted: true}, repo: "anyone/anything", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.client.Reaches(tt.repo); got != tt.want {
				t.Fatalf("%v.Reaches(%q) = %v, want %v", tt.client.Repositories, tt.repo, got, tt.want)
			}
		})
	}
}

// repoSegment is a generated owner or name: mostly a valid one, sometimes
// one a repository may not have.
type repoSegment string

const segmentAlphabet = "abcXYZ019_.-"

func (repoSegment) Generate(r *rand.Rand, size int) reflect.Value {
	odd := []string{"", ".", "..", "*", "a/b", "/", " ", "K", "%2e", "é"}
	if r.Intn(4) == 0 {
		return reflect.ValueOf(repoSegment(odd[r.Intn(len(odd))]))
	}
	n := 1 + r.Intn(max(size, 1)%12+1)
	var b strings.Builder
	for range n {
		b.WriteByte(segmentAlphabet[r.Intn(len(segmentAlphabet))])
	}
	return reflect.ValueOf(repoSegment(b.String()))
}

// validSegment is the specification of an owner or a name: ASCII letters,
// digits, '.', '_' and '-', neither "." nor "..".
func validSegment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for i := range len(s) {
		if !strings.ContainsRune(segmentAlphabet+"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", rune(s[i])) {
			return false
		}
	}
	return true
}

// mixCase flips the case of every other ASCII letter.
func mixCase(s string) string {
	b := []byte(s)
	for i := 0; i < len(b); i += 2 {
		switch {
		case b[i] >= 'a' && b[i] <= 'z':
			b[i] -= 'a' - 'A'
		case b[i] >= 'A' && b[i] <= 'Z':
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// An owner/* pattern reaches a repository exactly when it is one valid
// owner, the pattern's in any case, and one valid name.
func TestClient_reaches_property_ownerWildcard(t *testing.T) {
	t.Parallel()
	prop := func(patternOwner, owner, name repoSegment, sameOwner bool) bool {
		if !validSegment(string(patternOwner)) {
			return true
		}
		if sameOwner {
			owner = repoSegment(mixCase(string(patternOwner)))
		}
		c := domain.Client{Name: "p", Repositories: []string{string(patternOwner) + "/*"}}
		want := validSegment(string(owner)) && validSegment(string(name)) &&
			strings.EqualFold(string(owner), string(patternOwner))
		return c.Reaches(string(owner)+"/"+string(name)) == want
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 5000}); err != nil {
		t.Fatal(err)
	}
}

// An owner/* pattern never reaches a repository of another owner, nor a
// string that is not one owner and one name.
func TestClient_reaches_property_ownerWildcardNeverLeaksAnotherOwner(t *testing.T) {
	t.Parallel()
	prop := func(patternOwner, owner, name, extra repoSegment) bool {
		c := domain.Client{Name: "p", Repositories: []string{string(patternOwner) + "/*"}}
		if !strings.EqualFold(string(owner), string(patternOwner)) && c.Reaches(string(owner)+"/"+string(name)) {
			return false
		}
		return !c.Reaches(string(owner) + "/" + string(name) + "/" + string(extra))
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 5000}); err != nil {
		t.Fatal(err)
	}
}
