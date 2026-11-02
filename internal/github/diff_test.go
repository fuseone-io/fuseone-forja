package github_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/github"
)

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
	for _, head := range []string{"", "--output=/tmp/x", "HEAD", agentBranch, co.SHA() + "..HEAD"} {
		if n, err := co.DiffLines(context.Background(), head); err == nil {
			t.Errorf("DiffLines(%q) = %d, nil; want a refusal", head, n)
		}
	}
}
