package domain_test

import (
	"reflect"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// pilotLists are the pilot contract's proof_paths and human_merge_paths.
func pilotLists() domain.Contract {
	return domain.Contract{
		ProofPaths:      []string{".forja.yaml", "Makefile", ".golangci.yml", "**/testdata/**", "**/*_test.go"},
		HumanMergePaths: []string{".github/**", "GNUmakefile", "makefile"},
	}
}

func change(path string, kind domain.ChangeKind) domain.FileChange {
	return domain.FileChange{Path: path, Kind: kind, Added: 3, Deleted: 1}
}

func TestClassifyProof_pilotLists_existingProofChangedOpensSectionNewDoesNot(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change domain.FileChange
		proof  bool
	}{
		{"new test file", change("pkg/new_test.go", domain.ChangeAdded), false},
		{"existing test modified", change("pkg/old_test.go", domain.ChangeModified), true},
		{"existing test deleted", change("pkg/old_test.go", domain.ChangeDeleted), true},
		{"existing test type changed", change("pkg/old_test.go", domain.ChangeTypeChanged), true},
		{"new testdata file", change("x/testdata/f", domain.ChangeAdded), false},
		{"existing testdata modified", change("x/testdata/f", domain.ChangeModified), true},
		{"Makefile modified", change("Makefile", domain.ChangeModified), true},
		{"contract deleted", change(".forja.yaml", domain.ChangeDeleted), true},
		{"production code modified", change("internal/x.go", domain.ChangeModified), false},
		{"Makefile below the root", change("sub/Makefile", domain.ChangeModified), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := domain.ClassifyProof(pilotLists(), []domain.FileChange{tt.change})
			var want domain.Proof
			if tt.proof {
				want.ProofPaths = []domain.FileChange{tt.change}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("ClassifyProof(%+v) = %+v, want %+v", tt.change, got, want)
			}
			if got.HumanMerge() != tt.proof {
				t.Fatalf("HumanMerge() = %v, want %v", got.HumanMerge(), tt.proof)
			}
		})
	}
}

func TestClassifyProof_addedUnderHumanMergePath_humanMerge(t *testing.T) {
	t.Parallel()
	for _, kind := range []domain.ChangeKind{domain.ChangeAdded, domain.ChangeModified, domain.ChangeDeleted, domain.ChangeTypeChanged} {
		c := change(".github/CODEOWNERS", kind)
		got := domain.ClassifyProof(pilotLists(), []domain.FileChange{c})
		want := domain.Proof{HumanMergePaths: []domain.FileChange{c}}
		if !reflect.DeepEqual(got, want) || !got.HumanMerge() {
			t.Fatalf("%s: ClassifyProof = %+v (human merge %v), want %+v", kind, got, got.HumanMerge(), want)
		}
	}
}

// A path in both lists is in both, and the proof list still exempts what
// the agent added.
func TestClassifyProof_pathInBothLists_listedInEachThatApplies(t *testing.T) {
	t.Parallel()
	c := domain.Contract{ProofPaths: []string{".github/**"}, HumanMergePaths: []string{"**/*.yml"}}
	modified := change(".github/workflows/ci.yml", domain.ChangeModified)
	added := change(".github/workflows/new.yml", domain.ChangeAdded)

	got := domain.ClassifyProof(c, []domain.FileChange{modified, added})
	want := domain.Proof{ProofPaths: []domain.FileChange{modified}, HumanMergePaths: []domain.FileChange{modified, added}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ClassifyProof = %+v, want %+v", got, want)
	}
}

func TestClassifyProof_manyChanges_inputOrderKept(t *testing.T) {
	t.Parallel()
	changes := []domain.FileChange{
		change("z_test.go", domain.ChangeModified),
		change("internal/a.go", domain.ChangeModified),
		change(".github/b", domain.ChangeAdded),
		change("Makefile", domain.ChangeModified),
		change(".github/a", domain.ChangeDeleted),
	}
	got := domain.ClassifyProof(pilotLists(), changes)
	want := domain.Proof{
		ProofPaths:      []domain.FileChange{changes[0], changes[3]},
		HumanMergePaths: []domain.FileChange{changes[2], changes[4]},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ClassifyProof = %+v, want %+v", got, want)
	}
}

func TestClassifyProof_emptyLists_nothingMarked(t *testing.T) {
	t.Parallel()
	got := domain.ClassifyProof(domain.Contract{}, []domain.FileChange{change("Makefile", domain.ChangeModified)})
	if !reflect.DeepEqual(got, domain.Proof{}) || got.HumanMerge() {
		t.Fatalf("ClassifyProof with no lists = %+v, want nothing", got)
	}
}
