package github_test

import (
	"context"
	"errors"
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

// adoptedCheckout is the control plane's clone with the agent's commits,
// made by work on a copy of it, adopted from their bundle.
func adoptedCheckout(t *testing.T, work func(ws string)) (*github.Checkout, string) {
	t.Helper()
	_, co := startedCheckout(t)
	ws := workspaceCopy(t, co)
	work(ws)
	head, err := co.Adopt(context.Background(), exportBundle(t, ws, co.SHA(), agentBranch), agentBranch)
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	return co, head
}

func diffLines(t *testing.T, co *github.Checkout, head string) int {
	t.Helper()
	n, err := co.DiffLines(context.Background(), head)
	if err != nil {
		t.Fatalf("DiffLines: %v", err)
	}
	return n
}

// Every line the agent's commits add and delete counts, summed over the
// whole branch against the base, not only its last commit.
func TestCheckout_diffLines_addedPlusDeletedAgainstBase(t *testing.T) {
	t.Parallel()
	co, head := adoptedCheckout(t, func(ws string) {
		agentCommit(t, ws, map[string]string{"new.txt": "one\ntwo\nthree\n"}, "add three lines")
		runGit(t, ws, "rm", "-q", "docs/z.md")
		agentCommit(t, ws, map[string]string{"docs/a.md": "A\n"}, "change one line, delete a file")
	})

	// new.txt +3, docs/a.md +1 -1, docs/z.md -1: 4 added, 2 deleted.
	if got := diffLines(t, co, head); got != 6 {
		t.Fatalf("DiffLines = %d, want 6 (4 added + 2 deleted)", got)
	}
}

// A rename is a delete and an add, whatever git would detect: the count
// cannot be shrunk by moving a file.
func TestCheckout_diffLines_renameCountsBothSides(t *testing.T) {
	t.Parallel()
	co, head := adoptedCheckout(t, func(ws string) {
		runGit(t, ws, "mv", "docs/b.md", "docs/renamed.md")
		agentCommit(t, ws, nil, "rename")
	})
	runGit(t, co.Dir(), "config", "diff.renames", "copies")

	if got := diffLines(t, co, head); got != 2 {
		t.Fatalf("DiffLines = %d, want 2: the deleted line and the added one", got)
	}
}

// A binary file has no lines; the text beside it still counts.
func TestCheckout_diffLines_binaryCountsZero(t *testing.T) {
	t.Parallel()
	co, head := adoptedCheckout(t, func(ws string) {
		agentCommit(t, ws, map[string]string{"blob.bin": "\x00\x01\x02\nnot\ntext\n", "notes.txt": "a\nb\n"}, "binary and text")
	})

	if got := diffLines(t, co, head); got != 2 {
		t.Fatalf("DiffLines = %d, want 2: notes.txt only", got)
	}
}

// Whether a file is text, and how it is diffed, is the base's to say, not
// the agent's: a .gitattributes the agent committed cannot turn its lines
// into a binary that counts zero.
func TestCheckout_diffLines_agentGitattributesBinary_linesStillCount(t *testing.T) {
	t.Parallel()
	co, head := adoptedCheckout(t, func(ws string) {
		agentCommit(t, ws, map[string]string{".gitattributes": "* binary\n", "big.txt": "1\n2\n3\n"}, "hide the lines")
	})

	if got := diffLines(t, co, head); got != 4 {
		t.Fatalf("DiffLines = %d, want 4: big.txt and .gitattributes as text", got)
	}
}

// A git that does not know the attribute source would read the agent's
// .gitattributes without a word: the count is refused, never taken under
// the agent's attributes.
func TestCheckout_diffLines_gitWithoutAttrSource_refused(t *testing.T) {
	t.Parallel()
	repo := newBareRepo(t, "main")
	repo.commit(pilotFiles, "pilot")
	bin := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\nunset GIT_ATTR_SOURCE\ncase \"$1\" in --attr-source*) echo \"unknown option: $1\" >&2; exit 129;; esac\nexec git \"$@\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write stand-in: %v", err)
	}
	cloner := &github.Cloner{Tokens: &countingTokens{value: cloneToken}, Dir: t.TempDir(), Remote: repo.remote(), Git: bin}
	co, err := cloner.Clone(context.Background(), fakeRepo, "main")
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	t.Cleanup(func() { _ = co.Close() })
	if err := co.StartBranch(context.Background(), agentBranch); err != nil {
		t.Fatal(err)
	}
	ws := workspaceCopy(t, co)
	agentCommit(t, ws, map[string]string{".gitattributes": "* binary\n", "big.txt": "1\n2\n3\n"}, "hide the lines")
	head, err := co.Adopt(context.Background(), exportBundle(t, ws, co.SHA(), agentBranch), agentBranch)
	if err != nil {
		t.Fatal(err)
	}

	if n, err := co.DiffLines(context.Background(), head); err == nil {
		t.Fatalf("DiffLines = %d, nil under a git that ignores the attribute source; want a refusal", n)
	}
}

// No diff driver runs on the control plane, whether the attribute naming
// it is the agent's or the base's: the count is of the raw lines.
func TestCheckout_diffLines_gitattributesDiffDriverNotRun(t *testing.T) {
	t.Parallel()
	for name, baseAttributes := range map[string]bool{"agent's attributes": false, "base's attributes": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo := newBareRepo(t, "main")
			files := map[string]string{".forja.yaml": pilotFiles[".forja.yaml"]}
			if baseAttributes {
				files[".gitattributes"] = "*.txt diff=evil\n"
			}
			repo.commit(files, "base")
			co := cloneOf(t, repo, "main")
			if err := co.StartBranch(context.Background(), agentBranch); err != nil {
				t.Fatal(err)
			}
			ws := workspaceCopy(t, co)
			agent := map[string]string{"x.txt": "1\n2\n"}
			if !baseAttributes {
				agent[".gitattributes"] = "*.txt diff=evil\n"
			}
			agentCommit(t, ws, agent, "named driver")
			head, err := co.Adopt(context.Background(), exportBundle(t, ws, co.SHA(), agentBranch), agentBranch)
			if err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(t.TempDir(), "driver-ran")
			script := filepath.Join(t.TempDir(), "driver.sh")
			if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch '"+marker+"'\nseq 1 500\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			runGit(t, co.Dir(), "config", "diff.evil.textconv", script)
			runGit(t, co.Dir(), "config", "diff.evil.command", script)
			runGit(t, co.Dir(), "config", "diff.external", script)

			want := 2
			if !baseAttributes {
				want = 3
			}
			if got := diffLines(t, co, head); got != want {
				t.Fatalf("DiffLines = %d, want the %d raw lines", got, want)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("a diff driver ran on the control plane: %v", err)
			}
		})
	}
}

// Only a full commit name is diffed; anything else could be read as an
// option or a range.
func TestCheckout_diffLines_headNotACommitName_refused(t *testing.T) {
	t.Parallel()
	co, _ := adoptedCheckout(t, func(ws string) {
		agentCommit(t, ws, map[string]string{"x": "x\n"}, "one")
	})
	option := "--output=" + filepath.Join(t.TempDir(), "out")
	for _, head := range []string{"", option, "HEAD", agentBranch, co.SHA() + "..HEAD"} {
		if n, err := co.DiffLines(context.Background(), head); err == nil {
			t.Errorf("DiffLines(%q) = %d, nil; want a refusal", head, n)
		}
	}
}
