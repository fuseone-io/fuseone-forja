package domain_test

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"testing/quick"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

func access(i int) domain.RegistryAccess {
	return domain.RegistryAccess{
		At:       time.Date(2026, 11, 2, 9, 0, i, 0, time.UTC),
		Registry: "ghcr.io/my-org",
		Path:     fmt.Sprintf("/pkg-%d", i),
		Outcome:  domain.RegistryAllowed,
		Status:   200,
	}
}

func TestRegistryReport_overHundred_countExactListCapped(t *testing.T) {
	t.Parallel()
	tests := []struct {
		n         int
		truncated bool
	}{
		{n: 0},
		{n: domain.MaxRegistryAccesses},
		{n: domain.MaxRegistryAccesses + 1, truncated: true},
		{n: 3 * domain.MaxRegistryAccesses, truncated: true},
	}
	for _, tt := range tests {
		var r domain.RegistryReport
		for i := range tt.n {
			r.Record(access(i))
		}
		kept := min(tt.n, domain.MaxRegistryAccesses)
		if r.Count != tt.n || len(r.Accesses) != kept || r.Truncated() != tt.truncated {
			t.Errorf("n=%d: count %d, kept %d, truncated %v; want %d, %d, %v", tt.n, r.Count, len(r.Accesses), r.Truncated(), tt.n, kept, tt.truncated)
		}
		for i, a := range r.Accesses {
			if a != access(i) {
				t.Errorf("n=%d: Accesses[%d] = %+v, want the %d-th recorded", tt.n, i, a, i)
				break
			}
		}
	}
}

func TestValidRegistry_wellFormed_accepted(t *testing.T) {
	t.Parallel()
	for _, entry := range []string{
		"proxy.golang.org",
		"ghcr.io/my-org",
		"npm.pkg.github.com",
		"registry.example.com:8443/team/sub_area",
		"github.com/fuseone-io",
		"10.0.0.5:5000",
		"a.b-c/d.e~f",
	} {
		if err := domain.ValidRegistry(entry); err != nil {
			t.Errorf("ValidRegistry(%q) = %v, want nil", entry, err)
		}
	}
}

func TestValidRegistry_malformed_refusedWithoutEchoingIt(t *testing.T) {
	t.Parallel()
	for name, entry := range map[string]string{
		"empty":             "",
		"scheme":            "https://ghcr.io/my-org",
		"query":             "ghcr.io/my-org?x=1",
		"fragment":          "ghcr.io/my-org#x",
		"userinfo":          "user:pw@ghcr.io",
		"wildcard host":     "*.github.com",
		"wildcard segment":  "ghcr.io/*",
		"dot dot segment":   "ghcr.io/my-org/../other",
		"dot segment":       "ghcr.io/./my-org",
		"trailing slash":    "ghcr.io/my-org/",
		"empty segment":     "ghcr.io//my-org",
		"uppercase":         "GHCR.io/my-org",
		"no host":           "/my-org",
		"port not a number": "ghcr.io:https/x",
		"port zero":         "ghcr.io:0",
		"port padded":       "ghcr.io:0443",
		"port too high":     "ghcr.io:65536",
		"host starts dot":   ".ghcr.io",
		"host starts dash":  "-ghcr.io",
		"percent":           "ghcr.io/my%2forg",
		"backslash":         `ghcr.io\my-org`,
		"space":             "ghcr.io/my org",
		"too long":          "a.io/" + strings.Repeat("x", 251),
	} {
		err := domain.ValidRegistry(entry)
		if err == nil {
			t.Errorf("%s: ValidRegistry(%q) = nil, want an error", name, entry)
			continue
		}
		if entry != "" && strings.Contains(err.Error(), entry) {
			t.Errorf("%s: error %q repeats the entry: a misplaced secret would be printed", name, err)
		}
	}
}

// segments is an alphabet chosen so that prefixes without a segment
// boundary ("a" against "ab") are as frequent as real matches.
var segments = []string{"a", "ab", "b", "a.b", "a-b"}

func randomPath(r *rand.Rand, maxSegments int) string {
	n := 1 + r.IntN(maxSegments)
	parts := make([]string, n)
	for i := range parts {
		parts[i] = segments[r.IntN(len(segments))]
	}
	return strings.Join(parts, "/")
}

// segmentPrefix is the oracle: every segment of entry equals the segment
// of path at the same position.
func segmentPrefix(entry, path string) bool {
	e, p := strings.Split(entry, "/"), strings.Split(path, "/")
	if len(e) > len(p) {
		return false
	}
	for i := range e {
		if e[i] != p[i] {
			return false
		}
	}
	return true
}

// MatchRegistry matches exactly when an entry is a segment-wise prefix of
// the path, and among several such entries returns the one with the most
// segments.
func TestMatchRegistry_matchIffSegmentBoundaryPrefix(t *testing.T) {
	t.Parallel()
	property := func(seed uint64) bool {
		r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
		entries := []string{randomPath(r, 3), randomPath(r, 3), randomPath(r, 3)}
		path := randomPath(r, 5)
		want, wantSegments := "", 0
		for _, e := range entries {
			if n := strings.Count(e, "/") + 1; segmentPrefix(e, path) && n > wantSegments {
				want, wantSegments = e, n
			}
		}
		got, ok := domain.MatchRegistry(entries, path)
		if ok != (want != "") || got != want {
			t.Logf("entries %q path %q: got (%q, %v), want (%q, %v)", entries, path, got, ok, want, want != "")
			return false
		}
		return true
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 2000}); err != nil {
		t.Fatal(err)
	}
}

func TestMatchRegistry_prefixWithoutSegmentBoundary_noMatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		path  string
		match string
	}{
		{path: "ghcr.io/my-org-evil/pkg"},
		{path: "ghcr.io/my-orgx"},
		{path: "ghcr.io"},
		{path: "ghcr.io.evil.com/my-org/pkg"},
		{path: "ghcr.io/my-org", match: "ghcr.io/my-org"},
		{path: "ghcr.io/my-org/pkg/v1", match: "ghcr.io/my-org"},
	}
	for _, tt := range tests {
		got, ok := domain.MatchRegistry([]string{"ghcr.io/my-org"}, tt.path)
		if ok != (tt.match != "") || got != tt.match {
			t.Errorf("MatchRegistry(ghcr.io/my-org, %q) = (%q, %v), want %q", tt.path, got, ok, tt.match)
		}
	}
}
