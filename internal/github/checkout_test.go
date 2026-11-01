package github_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/github"
)

func pilotCheckout(t *testing.T) (*bareRepo, *github.Checkout) {
	t.Helper()
	repo := newBareRepo(t, "main")
	repo.commit(pilotFiles, "pilot")
	return repo, cloneOf(t, repo, "main")
}

func TestCheckout_blobSHA_matchesGitHashObject(t *testing.T) {
	t.Parallel()
	repo, co := pilotCheckout(t)

	got, err := co.BlobSHA(context.Background(), ".forja.yaml")
	if err != nil {
		t.Fatalf("BlobSHA: %v", err)
	}
	if want := repo.hashObject(pilotFiles[".forja.yaml"]); got != want {
		t.Errorf("BlobSHA = %q, want hash-object %q", got, want)
	}
	if got == co.SHA() {
		t.Errorf("BlobSHA is the commit SHA, want the blob's")
	}
	if _, err := co.BlobSHA(context.Background(), "docs"); !errors.Is(err, github.ErrIsDirectory) {
		t.Errorf("BlobSHA(docs) error = %v, want ErrIsDirectory", err)
	}
}

func TestCheckout_readFile_bytesOfTheBlobAtTheSHA(t *testing.T) {
	t.Parallel()
	_, co := pilotCheckout(t)

	got, err := co.ReadFile(context.Background(), "CLAUDE.md")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != pilotFiles["CLAUDE.md"] {
		t.Errorf("ReadFile = %q, want %q", got, pilotFiles["CLAUDE.md"])
	}
}

func TestCheckout_pathMissing_errPathMissing(t *testing.T) {
	t.Parallel()
	_, co := pilotCheckout(t)
	ctx := context.Background()

	calls := map[string]func() error{
		"ReadFile":  func() error { _, err := co.ReadFile(ctx, "MISSING.md"); return err },
		"ListFiles": func() error { _, err := co.ListFiles(ctx, "missing/"); return err },
		"BlobSHA":   func() error { _, err := co.BlobSHA(ctx, "MISSING.md"); return err },
		// A prefix of an existing name is not that name.
		"prefix": func() error { _, err := co.ReadFile(ctx, "doc"); return err },
	}
	for name, call := range calls {
		err := call()
		if !errors.Is(err, github.ErrPathMissing) {
			t.Errorf("%s: error = %v, want ErrPathMissing", name, err)
			continue
		}
		if !strings.Contains(err.Error(), co.SHA()) {
			t.Errorf("%s: error %q does not name the commit", name, err)
		}
	}
}

func TestCheckout_directory_errIsDirectoryOnReadFileAndListFilesInTreeOrder(t *testing.T) {
	t.Parallel()
	_, co := pilotCheckout(t)
	ctx := context.Background()

	if _, err := co.ReadFile(ctx, "docs"); !errors.Is(err, github.ErrIsDirectory) {
		t.Errorf("ReadFile(docs) error = %v, want ErrIsDirectory", err)
	}
	// git orders a tree by name, with a subtree sorted as "name/".
	want := []string{"docs/a.md", "docs/b.md", "docs/sub/c.md", "docs/z.md"}
	for _, dir := range []string{"docs", "docs/"} {
		got, err := co.ListFiles(ctx, dir)
		if err != nil {
			t.Fatalf("ListFiles(%q): %v", dir, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ListFiles(%q) = %q, want %q", dir, got, want)
		}
	}
	if _, err := co.ListFiles(ctx, "CLAUDE.md"); err == nil || errors.Is(err, github.ErrPathMissing) {
		t.Errorf("ListFiles(CLAUDE.md) error = %v, want an error that is not ErrPathMissing", err)
	}
}
