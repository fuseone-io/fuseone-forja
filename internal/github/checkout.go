package github

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// modeSymlink is git's tree mode for a symbolic link.
const modeSymlink = "120000"

// Sentinels for a path asked of a Checkout.
var (
	ErrPathMissing = errors.New("path missing")
	ErrIsDirectory = errors.New("path is a directory")
)

// Checkout is a clone pinned at one commit. Every read goes through git
// against that commit, never through the working tree, so what it returns
// is what the commit holds.
type Checkout struct {
	dir    string
	sha    string
	branch string
	git    gitCmd
	env    []string
}

// openCheckout pins the commit and branch git checked out in dir.
func openCheckout(ctx context.Context, git gitCmd, dir string) (*Checkout, error) {
	env := baseEnv()
	sha, err := git.run(ctx, dir, env, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	branch, err := git.run(ctx, dir, env, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil, err
	}
	co := &Checkout{dir: dir, sha: strings.TrimSpace(string(sha)), branch: strings.TrimSpace(string(branch)), git: git, env: env}
	if co.sha == "" {
		return nil, errors.New("clone has no HEAD")
	}
	return co, nil
}

// Dir is the clone directory.
func (c *Checkout) Dir() string { return c.dir }

// SHA is the commit the checkout is pinned at.
func (c *Checkout) SHA() string { return c.sha }

// Branch is the name of the branch checked out.
func (c *Checkout) Branch() string { return c.branch }

// Close removes the clone directory.
func (c *Checkout) Close() error { return os.RemoveAll(c.dir) }

// ReadFile returns the bytes of a file at the commit.
func (c *Checkout) ReadFile(ctx context.Context, path string) ([]byte, error) {
	entry, err := c.stat(ctx, path)
	if err != nil {
		return nil, err
	}
	if err := entry.mustBeBlob(); err != nil {
		return nil, err
	}
	return c.git.run(ctx, c.dir, c.env, "cat-file", "blob", entry.sha)
}

// ListFiles returns the regular files under dir, recursively, as paths from
// the repository root in git's tree order.
func (c *Checkout) ListFiles(ctx context.Context, dir string) ([]string, error) {
	entry, err := c.stat(ctx, dir)
	if err != nil {
		return nil, err
	}
	if entry.typ != "tree" {
		return nil, fmt.Errorf("%s: is a %s, not a directory", entry.path, entry.typ)
	}
	out, err := c.git.run(ctx, c.dir, c.env, "ls-tree", "-r", "-z", c.sha, "--", entry.path)
	if err != nil {
		return nil, err
	}
	files := []string{}
	for _, e := range parseLsTree(out) {
		if e.typ == "blob" && e.mode != modeSymlink {
			files = append(files, e.path)
		}
	}
	return files, nil
}

// BlobSHA returns the SHA of the blob at path, which identifies the file's
// bytes regardless of the commit they sit in.
func (c *Checkout) BlobSHA(ctx context.Context, path string) (string, error) {
	entry, err := c.stat(ctx, path)
	if err != nil {
		return "", err
	}
	if err := entry.mustBeBlob(); err != nil {
		return "", err
	}
	return entry.sha, nil
}

// treeEntry is one record of git ls-tree.
type treeEntry struct {
	mode string
	typ  string
	sha  string
	path string
}

// mustBeBlob accepts a regular file. A symbolic link is a blob too, but
// its bytes are a path, not content.
func (e treeEntry) mustBeBlob() error {
	switch e.typ {
	case "blob":
		if e.mode == modeSymlink {
			return fmt.Errorf("%s: is a symbolic link, not a file", e.path)
		}
		return nil
	case "tree":
		return fmt.Errorf("%s: %w", e.path, ErrIsDirectory)
	default:
		return fmt.Errorf("%s: is a %s, not a file", e.path, e.typ)
	}
}

// stat resolves one path at the commit. A trailing slash is dropped
// because git would list the directory's children instead of the entry.
func (c *Checkout) stat(ctx context.Context, path string) (treeEntry, error) {
	path = strings.TrimSuffix(path, "/")
	out, err := c.git.run(ctx, c.dir, c.env, "ls-tree", "-z", c.sha, "--", path)
	if err != nil {
		return treeEntry{}, err
	}
	entries := parseLsTree(out)
	if len(entries) != 1 || entries[0].path != path {
		return treeEntry{}, fmt.Errorf("%s: %w at %s", path, ErrPathMissing, c.sha)
	}
	return entries[0], nil
}

// parseLsTree reads NUL-terminated "mode SP type SP sha TAB path" records.
func parseLsTree(out []byte) []treeEntry {
	var entries []treeEntry
	for _, rec := range bytes.Split(out, []byte{0}) {
		meta, path, ok := bytes.Cut(rec, []byte{'\t'})
		if !ok {
			continue
		}
		fields := strings.Fields(string(meta))
		if len(fields) != 3 {
			continue
		}
		entries = append(entries, treeEntry{mode: fields[0], typ: fields[1], sha: fields[2], path: string(path)})
	}
	return entries
}
