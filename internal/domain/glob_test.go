package domain_test

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"testing/quick"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// The PRD's verify.exclude and proof_paths examples, and the pilot's.
func TestMatchPath_contractExamples_matchOnSegments(t *testing.T) {
	t.Parallel()
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"**/*.md", "README.md", true},
		{"**/*.md", "docs/adr/x.md", true},
		{"**/*.md", "x.mdx", false},
		{"**/*.md", "docs/x.md/y.go", false},
		{"migrations/**", "migrations/a/b.sql", true},
		{"migrations/**", "migrations", true},
		{"migrations/**", "db/migrations/a.sql", false},
		{"docs/**", "docs/adr/0042-implement-delivery.md", true},
		{"scripts/**", "scripts/gates.sh", true},
		{"**/testdata/**", "internal/contract/testdata/prd-example.yaml", true},
		{"**/testdata/**", "testdata/x", true},
		{"**/testdata/**", "internal/testdatafile.go", false},
		{"**/*_test.go", "internal/mcp/verify_test.go", true},
		{"**/*_test.go", "main_test.go", true},
		{"**/*_test.go", "internal/mcp/verify.go", false},
		{".github/**", ".github/workflows/ci.yml", true},
		{".forja.yaml", ".forja.yaml", true},
		{".forja.yaml", "sub/.forja.yaml", false},
		{"*.go", "main.go", true},
		{"*.go", "cmd/main.go", false},
		{"internal/*/x.go", "internal/mcp/x.go", true},
		{"internal/*/x.go", "internal/a/b/x.go", false},
		{"a/**/b", "a/b", true},
		{"a/**/b", "a/x/y/b", true},
		{"a/**/b", "a/x/y/c", false},
	}
	for _, tt := range tests {
		got, ok := domain.MatchPath([]string{tt.pattern}, tt.path)
		if ok != tt.want {
			t.Errorf("MatchPath(%q, %q) = %v, want %v", tt.pattern, tt.path, ok, tt.want)
		}
		if ok && got != tt.pattern {
			t.Errorf("MatchPath(%q, %q) named %q, want the pattern", tt.pattern, tt.path, got)
		}
	}
}

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
		if got := matches(tt.pattern, tt.path); got != tt.want {
			t.Errorf("MatchPath(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
		}
	}
}

func TestMatchPath_noPatternMatches_emptyAndFalse(t *testing.T) {
	t.Parallel()
	got, ok := domain.MatchPath([]string{"docs/**", "*.md"}, "internal/x.go")
	if ok || got != "" {
		t.Fatalf("MatchPath = %q, %v; want \"\", false", got, ok)
	}
	if got, ok := domain.MatchPath(nil, "README.md"); ok || got != "" {
		t.Fatalf("MatchPath(nil) = %q, %v; want \"\", false", got, ok)
	}
}

// A segment path.Match refuses matches nothing, even a path spelled like
// it: a bad verify.exclude entry must never exclude production code.
func TestMatchPath_malformedSegment_matchesNothing(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ pattern, path string }{
		{"[", "["},
		{"docs/[", "docs/x"},
		{"a[/]b", "a[/]b"},
	} {
		if got, ok := domain.MatchPath([]string{tt.pattern}, tt.path); ok {
			t.Errorf("MatchPath(%q, %q) = %q, true; want no match", tt.pattern, tt.path, got)
		}
	}
}

// The pattern named is one that matches, the first of them in the list.
func TestMatchPath_severalMatch_firstNamed(t *testing.T) {
	t.Parallel()
	got, ok := domain.MatchPath([]string{"*.go", "docs/**", "**/*.md"}, "docs/x.md")
	if !ok || got != "docs/**" {
		t.Fatalf("MatchPath = %q, %v; want docs/**", got, ok)
	}
}

// segmentsOf turns arbitrary input into a path of 1 to 6 non-empty
// segments free of glob metacharacters.
func segmentsOf(raw []string) []string {
	var segs []string
	for _, s := range raw {
		s = strings.Map(func(r rune) rune {
			if strings.ContainsRune("/*?[]\\", r) {
				return -1
			}
			return r
		}, s)
		if s != "" {
			segs = append(segs, s)
		}
		if len(segs) == 6 {
			break
		}
	}
	if len(segs) == 0 {
		segs = []string{"x"}
	}
	return segs
}

func TestMatchPath_property_literalMatchesItself(t *testing.T) {
	t.Parallel()
	prop := func(raw []string) bool {
		p := strings.Join(segmentsOf(raw), "/")
		_, ok := domain.MatchPath([]string{p}, p)
		return ok
	}
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}

func TestMatchPath_property_doubleStarMatchesEveryPath(t *testing.T) {
	t.Parallel()
	prop := func(raw []string) bool {
		_, ok := domain.MatchPath([]string{"**"}, strings.Join(segmentsOf(raw), "/"))
		return ok
	}
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}

// A single star stays inside one segment: on a path of two segments or
// more it never matches, and "<first>/*" never reaches a third segment.
func TestMatchPath_property_starNeverCrossesSlash(t *testing.T) {
	t.Parallel()
	prop := func(raw []string) bool {
		segs := segmentsOf(raw)
		p := strings.Join(segs, "/")
		_, star := domain.MatchPath([]string{"*"}, p)
		_, under := domain.MatchPath([]string{segs[0] + "/*"}, p)
		return star == (len(segs) == 1) && under == (len(segs) == 2)
	}
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}

func matches(pattern, p string) bool {
	_, ok := domain.MatchPath([]string{pattern}, p)
	return ok
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

// checkPath takes any because quick.Check accepts a function of any arity.
func checkPath(t *testing.T, property any) {
	t.Helper()
	if err := quick.Check(property, &quick.Config{MaxCount: 500, Rand: rand.New(rand.NewSource(18))}); err != nil {
		t.Fatal(err)
	}
}

func TestMatchPath_property_literalMatchesOnlyItself(t *testing.T) {
	t.Parallel()
	checkPath(t, func(a, b repoPath) bool {
		return matches(string(a), string(a)) && matches(string(a), string(b)) == (a == b)
	})
}

func TestMatchPath_property_dirDoubleStarMatchesEveryPathUnderIt(t *testing.T) {
	t.Parallel()
	checkPath(t, func(dir, rest, other repoPath) bool {
		under := matches(string(dir)+"/**", string(dir)+"/"+string(rest))
		outside := strings.HasPrefix(string(other)+"/", string(dir)+"/") || !matches(string(dir)+"/**", string(other)+"/"+string(rest))
		return under && outside
	})
}

func TestMatchPath_property_doubleStarBaseMatchesEveryPathEndingInIt(t *testing.T) {
	t.Parallel()
	checkPath(t, func(dir, p repoPath) bool {
		base := lastSegment(string(p))
		ending := matches("**/"+base, string(dir)+"/"+base) && matches("**/"+base, base)
		return ending && matches("**/"+base, string(dir)) == (lastSegment(string(dir)) == base)
	})
}

func lastSegment(p string) string {
	return p[strings.LastIndexByte(p, '/')+1:]
}
