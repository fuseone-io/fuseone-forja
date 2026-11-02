package domain_test

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"testing/quick"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

func TestMatchPath_patternsOfThePilot_anchoredAtTheRoot(t *testing.T) {
	t.Parallel()
	tests := []struct {
		pattern, path string
		want          bool
	}{
		{"Makefile", "Makefile", true},
		{"Makefile", "sub/Makefile", false},
		{"Makefile", "Makefile.bak", false},
		{".forja.yaml", ".forja.yaml", true},
		{".forja.yaml", "x/.forja.yaml", false},
		{"**/*_test.go", "main_test.go", true},
		{"**/*_test.go", "internal/domain/glob_test.go", true},
		{"**/*_test.go", ".hidden/x_test.go", true},
		{"**/*_test.go", "internal/domain/glob.go", false},
		{"**/*_test.go", "a_test.go/readme", false},
		{"*_test.go", "pkg/a_test.go", false},
		{"*.go", "a/b.go", false},
		{".github/**", ".github/CODEOWNERS", true},
		{".github/**", ".github/workflows/ci.yml", true},
		{".github/**", "x/.github/CODEOWNERS", false},
		{".github/**", ".githubx/CODEOWNERS", false},
		{"**/testdata/**", "testdata/f", true},
		{"**/testdata/**", "x/testdata/f", true},
		{"**/testdata/**", "a/b/testdata/c/d", true},
		{"**/testdata/**", "x/testdatax/f", false},
		{"**/testdata/**", "x/mytestdata/f", false},
		{"docs/**/*.md", "docs/a.md", true},
		{"docs/**/*.md", "docs/sub/deep/a.md", true},
		{"docs/**/*.md", "docs/sub/a.txt", false},
		{"scripts/*", "scripts/gates.sh", true},
		{"scripts/*", "scripts/sub/gates.sh", false},
		{"[abc]*.go", "b.go", true},
		{"[abc]*.go", "d.go", false},
		{"a?c", "abc", true},
		{"a?c", "a/c", false},
		{"[abc", "[abc", false},
		{"**/[abc", "x/[abc", false},
	}
	for _, tt := range tests {
		if got := domain.MatchPath(tt.pattern, tt.path); got != tt.want {
			t.Errorf("MatchPath(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
		}
	}
}

// repoPath generates a path of git's form: one to five non-empty segments
// of characters no glob gives a meaning to.
type repoPath string

const pathAlphabet = "abcxyz019._-"

func segment(r *rand.Rand) string {
	var b strings.Builder
	for i := 0; i < 1+r.Intn(6); i++ {
		b.WriteByte(pathAlphabet[r.Intn(len(pathAlphabet))])
	}
	return b.String()
}

func (repoPath) Generate(r *rand.Rand, _ int) reflect.Value {
	segs := make([]string, 1+r.Intn(5))
	for i := range segs {
		segs[i] = segment(r)
	}
	return reflect.ValueOf(repoPath(strings.Join(segs, "/")))
}

func checkPath(t *testing.T, property any) {
	t.Helper()
	if err := quick.Check(property, &quick.Config{MaxCount: 500, Rand: rand.New(rand.NewSource(18))}); err != nil {
		t.Fatal(err)
	}
}

func TestMatchPath_property_doubleStarMatchesEveryPath(t *testing.T) {
	t.Parallel()
	checkPath(t, func(p repoPath) bool { return domain.MatchPath("**", string(p)) })
}

func TestMatchPath_property_literalMatchesOnlyItself(t *testing.T) {
	t.Parallel()
	checkPath(t, func(a, b repoPath) bool {
		return domain.MatchPath(string(a), string(a)) && domain.MatchPath(string(a), string(b)) == (a == b)
	})
}

func TestMatchPath_property_dirDoubleStarMatchesEveryPathUnderIt(t *testing.T) {
	t.Parallel()
	checkPath(t, func(dir, rest, other repoPath) bool {
		under := domain.MatchPath(string(dir)+"/**", string(dir)+"/"+string(rest))
		outside := strings.HasPrefix(string(other)+"/", string(dir)+"/") || !domain.MatchPath(string(dir)+"/**", string(other)+"/"+string(rest))
		return under && outside
	})
}

func TestMatchPath_property_doubleStarBaseMatchesEveryPathEndingInIt(t *testing.T) {
	t.Parallel()
	checkPath(t, func(dir, p repoPath) bool {
		base := lastSegment(string(p))
		ending := domain.MatchPath("**/"+base, string(dir)+"/"+base) && domain.MatchPath("**/"+base, base)
		return ending && domain.MatchPath("**/"+base, string(dir)) == (lastSegment(string(dir)) == base)
	})
}

func lastSegment(p string) string {
	return p[strings.LastIndexByte(p, '/')+1:]
}
