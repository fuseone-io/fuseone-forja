package github_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/github"
)

// diffBase is the pilot plus an existing test and a binary, the base the
// agent's diffs are taken against.
var diffBase = map[string]string{
	"pkg/old_test.go": "package pkg\n\nfunc TestOld(t *testing.T) {}\n",
	"img.bin":         "\x00\x01\x02binary\x00",
}

// diffCheckout is the control plane's clone of the pilot plus diffBase,
// with the agent's branch started and a workspace copy of it.
func diffCheckout(t *testing.T) (*github.Checkout, string) {
	t.Helper()
	repo := newBareRepo(t, "main")
	files := map[string]string{}
	for name, content := range pilotFiles {
		files[name] = content
	}
	for name, content := range diffBase {
		files[name] = content
	}
	repo.commit(files, "pilot")
	co := cloneOf(t, repo, "main")
	if err := co.StartBranch(context.Background(), agentBranch); err != nil {
		t.Fatalf("StartBranch: %v", err)
	}
	return co, workspaceCopy(t, co)
}

// adoptedDiff adopts what the workspace committed and diffs it against the
// base.
func adoptedDiff(t *testing.T, co *github.Checkout, ws string) []domain.FileChange {
	t.Helper()
	head, err := co.Adopt(context.Background(), exportBundle(t, ws, co.SHA(), agentBranch), agentBranch)
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	changes, err := co.Diff(context.Background(), head)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	return changes
}

func inWorkspace(t *testing.T, ws, name string) string {
	t.Helper()
	return filepath.Join(ws, filepath.FromSlash(name))
}

func TestCheckout_diff_eachKindOfChangeWithLineCounts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(t *testing.T, ws string)
		want   []domain.FileChange
	}{
		{"modified", func(t *testing.T, ws string) {
			agentCommit(t, ws, map[string]string{"docs/a.md": "x\ny\nz\n"}, "edit")
		}, []domain.FileChange{{Path: "docs/a.md", Kind: domain.ChangeModified, Added: 3, Deleted: 1}}},
		{"added", func(t *testing.T, ws string) {
			agentCommit(t, ws, map[string]string{"pkg/new_test.go": "package pkg\n"}, "add")
		}, []domain.FileChange{{Path: "pkg/new_test.go", Kind: domain.ChangeAdded, Added: 1}}},
		{"deleted", func(t *testing.T, ws string) {
			if err := os.Remove(inWorkspace(t, ws, "pkg/old_test.go")); err != nil {
				t.Fatal(err)
			}
			agentCommit(t, ws, nil, "delete")
		}, []domain.FileChange{{Path: "pkg/old_test.go", Kind: domain.ChangeDeleted, Deleted: 3}}},
		{"binary", func(t *testing.T, ws string) {
			agentCommit(t, ws, map[string]string{"img.bin": "\x00\x09other\x00"}, "binary")
		}, []domain.FileChange{{Path: "img.bin", Kind: domain.ChangeModified, Binary: true}}},
		{"mode only", func(t *testing.T, ws string) {
			if err := os.Chmod(inWorkspace(t, ws, "CLAUDE.md"), 0o755); err != nil {
				t.Fatal(err)
			}
			agentCommit(t, ws, nil, "chmod")
		}, []domain.FileChange{{Path: "CLAUDE.md", Kind: domain.ChangeModified}}},
		{"file to symbolic link", func(t *testing.T, ws string) {
			if err := os.Remove(inWorkspace(t, ws, "CONTRIBUTING.md")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("CLAUDE.md", inWorkspace(t, ws, "CONTRIBUTING.md")); err != nil {
				t.Fatal(err)
			}
			agentCommit(t, ws, nil, "link")
		}, []domain.FileChange{{Path: "CONTRIBUTING.md", Kind: domain.ChangeTypeChanged, Added: 1, Deleted: 1}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			co, ws := diffCheckout(t)
			tt.change(t, ws)

			if got := adoptedDiff(t, co, ws); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Diff = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// With rename detection off, moving an existing test is the deletion of a
// file the base had and the addition of one it did not.
func TestCheckout_diff_renameOfExistingTest_deletionAndAddition(t *testing.T) {
	t.Parallel()
	co, ws := diffCheckout(t)
	if err := os.Rename(inWorkspace(t, ws, "pkg/old_test.go"), inWorkspace(t, ws, "pkg/moved_test.go")); err != nil {
		t.Fatal(err)
	}
	agentCommit(t, ws, nil, "move")

	want := []domain.FileChange{
		{Path: "pkg/moved_test.go", Kind: domain.ChangeAdded, Added: 3},
		{Path: "pkg/old_test.go", Kind: domain.ChangeDeleted, Deleted: 3},
	}
	if got := adoptedDiff(t, co, ws); !reflect.DeepEqual(got, want) {
		t.Fatalf("Diff = %+v, want %+v", got, want)
	}
}

// The diff is the base against the head over every commit of the branch:
// a file added then edited is an addition, and one edited then restored is
// no change at all.
func TestCheckout_diff_severalCommits_baseAgainstHead(t *testing.T) {
	t.Parallel()
	co, ws := diffCheckout(t)
	agentCommit(t, ws, map[string]string{"docs/new.md": "one\n", "docs/a.md": "changed\n"}, "one")
	agentCommit(t, ws, map[string]string{"docs/new.md": "one\ntwo\n", "docs/a.md": "a\n"}, "two")

	want := []domain.FileChange{{Path: "docs/new.md", Kind: domain.ChangeAdded, Added: 2}}
	if got := adoptedDiff(t, co, ws); !reflect.DeepEqual(got, want) {
		t.Fatalf("Diff = %+v, want %+v", got, want)
	}
}

// A path with a tab, a newline or a quote comes back as git stored it.
func TestCheckout_diff_pathWithControlCharacters_keptVerbatim(t *testing.T) {
	t.Parallel()
	co, ws := diffCheckout(t)
	name := "docs/odd\tname\n\"x\".md"
	agentCommit(t, ws, map[string]string{name: "x\n"}, "odd")

	want := []domain.FileChange{{Path: name, Kind: domain.ChangeAdded, Added: 1}}
	if got := adoptedDiff(t, co, ws); !reflect.DeepEqual(got, want) {
		t.Fatalf("Diff = %+v, want %+v", got, want)
	}
}

// A head that is not a full commit name is refused before git starts: an
// abbreviation git would resolve, a ref, and an option git would obey.
func TestCheckout_diff_headNotAFullCommitName_refusedBeforeGit(t *testing.T) {
	t.Parallel()
	co, ws := diffCheckout(t)
	head := agentCommit(t, ws, map[string]string{"docs/a.md": "x\n"}, "edit")
	if _, err := co.Adopt(context.Background(), exportBundle(t, ws, co.SHA(), agentBranch), agentBranch); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	leak := filepath.Join(t.TempDir(), "leak")
	for _, bad := range []string{head[:12], "HEAD", agentBranch, "--output=" + leak, strings.ToUpper(head), head + "\n", ""} {
		if got, err := co.Diff(context.Background(), bad); err == nil {
			t.Errorf("Diff(%q) = %+v, nil; want a refusal", bad, got)
		}
	}
	if _, err := os.Stat(leak); !os.IsNotExist(err) {
		t.Fatalf("git ran with an option for a head: %v", err)
	}
}

func TestCheckout_diff_unknownCommit_error(t *testing.T) {
	t.Parallel()
	co, _ := diffCheckout(t)
	if got, err := co.Diff(context.Background(), strings.Repeat("ab", 20)); err == nil {
		t.Fatalf("Diff(unknown) = %+v, nil; want an error", got)
	}
}
