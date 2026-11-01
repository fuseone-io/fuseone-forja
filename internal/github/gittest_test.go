package github_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitTestEnv keeps the developer's own configuration, hooks and signing out
// of every git command a test runs.
func gitTestEnv() []string {
	return append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
	)
}

// runGit runs one git command with the test environment and returns its
// trimmed stdout, failing the test on a non-zero exit.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitTestEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

// bareRepo is a bare repository at <root>/<owner>/<name>.git, served over
// file:// to a Cloner, plus a scratch work tree that commits into it.
type bareRepo struct {
	t    *testing.T
	root string
	path string
	work string
}

func newBareRepo(t *testing.T, defaultBranch string) *bareRepo {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, fakeOwner, fakeName+".git")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir bare: %v", err)
	}
	runGit(t, root, "init", "-q", "--bare", "--initial-branch="+defaultBranch, path)
	work := t.TempDir()
	runGit(t, work, "init", "-q", "--initial-branch="+defaultBranch)
	runGit(t, work, "remote", "add", "origin", path)
	return &bareRepo{t: t, root: root, path: path, work: work}
}

// remote is the Cloner.Remote that reaches this repository: a file:// URL
// so that --depth is honoured, unlike a plain path.
func (r *bareRepo) remote() string { return "file://" + r.root + "/" }

// commit writes files into the work tree, commits them on the current
// branch, pushes that branch to the bare repository and returns the SHA.
func (r *bareRepo) commit(files map[string]string, msg string) string {
	r.t.Helper()
	for name, content := range files {
		full := filepath.Join(r.work, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			r.t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			r.t.Fatalf("write %s: %v", name, err)
		}
	}
	runGit(r.t, r.work, "add", "-A")
	runGit(r.t, r.work, "-c", "user.name=forja", "-c", "user.email=forja@example.invalid", "commit", "-q", "-m", msg)
	runGit(r.t, r.work, "push", "-q", "origin", "HEAD")
	return runGit(r.t, r.work, "rev-parse", "HEAD")
}

// branch starts a new branch at the current commit of the work tree.
func (r *bareRepo) branch(name string) {
	r.t.Helper()
	runGit(r.t, r.work, "checkout", "-q", "-b", name)
}

// revParse resolves a ref in the bare repository.
func (r *bareRepo) revParse(ref string) string {
	r.t.Helper()
	return runGit(r.t, r.path, "rev-parse", ref)
}

// hashObject is the blob SHA git gives to content.
func (r *bareRepo) hashObject(content string) string {
	r.t.Helper()
	file := filepath.Join(r.t.TempDir(), "blob")
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		r.t.Fatalf("write blob: %v", err)
	}
	return runGit(r.t, r.path, "hash-object", file)
}

// refs is every ref of the bare repository with its target, byte for byte.
func (r *bareRepo) refs() string {
	r.t.Helper()
	return runGit(r.t, r.path, "for-each-ref")
}
