package verify_test

import (
	"reflect"
	"sort"
	"testing"
	"testing/quick"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/verify"
)

// pilotContract carries the pilot's verify.exclude and proof_paths.
func pilotContract() domain.Contract {
	return domain.Contract{
		Verify:     domain.Verify{Exclude: []string{"**/*.md", "docs/**"}},
		ProofPaths: []string{".forja.yaml", "Makefile", "**/testdata/**", "**/*_test.go", ".github/**"},
	}
}

func files(paths ...string) []domain.ChangedFile {
	out := make([]domain.ChangedFile, 0, len(paths))
	for _, p := range paths {
		out = append(out, domain.ChangedFile{Path: p})
	}
	return out
}

func notVerifiable(reason domain.NotVerifiableReason, excluded ...string) verify.Outcome {
	if excluded == nil {
		excluded = []string{}
	}
	return verify.Outcome{QA: domain.QA{Verdict: domain.VerdictNotVerifiable, Reason: reason, Excluded: excluded}}
}

func TestClassify_diffShapes_verdictReasonAndExcluded(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		files []domain.ChangedFile
		want  verify.Outcome
	}{
		{name: "no file", files: nil, want: notVerifiable(domain.NotVerifiableNoProductionCode)},
		{
			name:  "markdown only",
			files: files("docs/x.md", "README.md"),
			want:  notVerifiable(domain.NotVerifiableOnlyExcludedPaths, "README.md", "docs/x.md"),
		},
		{
			name:  "proof paths only",
			files: files("internal/mcp/verify_test.go", ".forja.yaml"),
			want:  notVerifiable(domain.NotVerifiableNoProductionCode),
		},
		{
			name:  "excluded and proof paths",
			files: files("README.md", "internal/mcp/verify_test.go"),
			want:  notVerifiable(domain.NotVerifiableNoProductionCode, "README.md"),
		},
		{
			name:  "production code",
			files: files("internal/mcp/verify.go", "README.md"),
			want:  verify.Outcome{Production: true, QA: domain.QA{Excluded: []string{"README.md"}}},
		},
		{
			name:  "rename from production to excluded",
			files: []domain.ChangedFile{{Path: "docs/verify.md", PreviousPath: "internal/verify/old.go"}},
			want:  verify.Outcome{Production: true, QA: domain.QA{Excluded: []string{"docs/verify.md"}}},
		},
		{
			name:  "rename within excluded",
			files: []domain.ChangedFile{{Path: "docs/b.md", PreviousPath: "docs/a.md"}},
			want:  notVerifiable(domain.NotVerifiableOnlyExcludedPaths, "docs/a.md", "docs/b.md"),
		},
		{
			name:  "rename from excluded to production",
			files: []domain.ChangedFile{{Path: "cmd/x.go", PreviousPath: "docs/x.md"}},
			want:  verify.Outcome{Production: true, QA: domain.QA{Excluded: []string{"docs/x.md"}}},
		},
		{
			name:  "same path twice",
			files: files("README.md", "README.md"),
			want:  notVerifiable(domain.NotVerifiableOnlyExcludedPaths, "README.md"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := verify.Classify(pilotContract(), tt.files); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Classify = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// A path that is both excluded and a proof path counts as excluded.
func TestClassify_excludedAndProof_excludedWins(t *testing.T) {
	t.Parallel()
	c := domain.Contract{Verify: domain.Verify{Exclude: []string{"**/*.md"}}, ProofPaths: []string{"docs/**"}}
	want := notVerifiable(domain.NotVerifiableOnlyExcludedPaths, "docs/x.md")
	if got := verify.Classify(c, files("docs/x.md")); !reflect.DeepEqual(got, want) {
		t.Fatalf("Classify = %+v, want %+v", got, want)
	}
}

// pathPool is what the property draws paths and patterns from, so that
// both match and miss happen often.
var (
	pathPool    = []string{"README.md", "docs/x.md", "main.go", "internal/a_test.go", "internal/a.go", "testdata/f", ".forja.yaml"}
	patternPool = []string{"**/*.md", "docs/**", "**/*_test.go", "**/testdata/**", ".forja.yaml", "*.go", "**"}
)

func pick[T any](pool []T, picks []uint8) []T {
	out := make([]T, 0, len(picks))
	for _, p := range picks {
		out = append(out, pool[int(p)%len(pool)])
	}
	return out
}

// Whatever the files and the contract, this slice never concludes bites
// nor does not bite: a diff is production with no verdict, or not
// verifiable with one of the two reasons, and the excluded list is sorted,
// without repeats, and made of changed paths only.
func TestClassify_property_neverBitesNorDoesNotBite(t *testing.T) {
	t.Parallel()
	prop := func(news, olds, exclude, proof []uint8) bool {
		var changed []domain.ChangedFile
		for i, p := range pick(pathPool, news) {
			f := domain.ChangedFile{Path: p}
			if i < len(olds) && olds[i]%2 == 0 {
				f.PreviousPath = pathPool[int(olds[i])%len(pathPool)]
			}
			changed = append(changed, f)
		}
		c := domain.Contract{Verify: domain.Verify{Exclude: pick(patternPool, exclude)}, ProofPaths: pick(patternPool, proof)}
		got := verify.Classify(c, changed)
		return verdictAllowed(got) && excludedWellFormed(got.QA.Excluded, changed)
	}
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}

func verdictAllowed(o verify.Outcome) bool {
	if o.Production {
		return o.QA.Verdict == "" && o.QA.Reason == ""
	}
	reasonKnown := o.QA.Reason == domain.NotVerifiableOnlyExcludedPaths || o.QA.Reason == domain.NotVerifiableNoProductionCode
	return o.QA.Verdict == domain.VerdictNotVerifiable && reasonKnown
}

func excludedWellFormed(excluded []string, changed []domain.ChangedFile) bool {
	if excluded == nil || !sort.StringsAreSorted(excluded) {
		return false
	}
	seen := map[string]bool{}
	for _, f := range changed {
		seen[f.Path], seen[f.PreviousPath] = true, true
	}
	for i, p := range excluded {
		if !seen[p] || p == "" || (i > 0 && excluded[i-1] == p) {
			return false
		}
	}
	return true
}
