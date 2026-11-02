package github_test

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/github"
)

const agentBranch = "forja/7"

// agentContract is the .forja.yaml an agent rewrote on its branch.
const agentContract = "version: 1\nimage: golang:1.26\ngates:\n  - name: build\n    run: 'true'\n"

// startedCheckout is the control plane's depth-1 clone of the pilot with
// the agent's branch started at the base.
func startedCheckout(t *testing.T) (*bareRepo, *github.Checkout) {
	t.Helper()
	repo, co := pilotCheckout(t)
	if err := co.StartBranch(context.Background(), agentBranch); err != nil {
		t.Fatalf("StartBranch: %v", err)
	}
	return repo, co
}

// workspaceCopy is what the agent workspace holds: a byte copy of the
// control plane's clone, .git included.
func workspaceCopy(t *testing.T, co *github.Checkout) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "workspace")
	if err := os.CopyFS(dir, os.DirFS(co.Dir())); err != nil {
		t.Fatalf("copy clone: %v", err)
	}
	return dir
}

// agentCommit writes files in the workspace copy and commits them on the
// branch checked out there, as the agent does.
func agentCommit(t *testing.T, ws string, files map[string]string, msg string) string {
	t.Helper()
	for name, content := range files {
		full := filepath.Join(ws, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, ws, "add", "-A")
	runGit(t, ws, "-c", "user.name=Forja agent", "-c", "user.email=agent@forja.invalid", "commit", "-q", "-m", msg)
	return runGit(t, ws, "rev-parse", "HEAD")
}

// exportBundle is the workspace's export: the agent's commits on branch
// beyond base, as one bundle file.
func exportBundle(t *testing.T, ws, base, branch string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "commits.bundle")
	runGit(t, ws, "bundle", "create", "-q", path, base+"..refs/heads/"+branch)
	return path
}

// prerequisites are the "-<sha>" lines of a v2 bundle's header.
func prerequisites(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() && sc.Text() != "" {
		if sha, ok := strings.CutPrefix(sc.Text(), "-"); ok {
			out = append(out, strings.Fields(sha)[0])
		}
	}
	return out
}

func TestCheckout_startBranch_branchCheckedOutAtBase(t *testing.T) {
	t.Parallel()
	_, co := startedCheckout(t)

	if got := runGit(t, co.Dir(), "rev-parse", "--abbrev-ref", "HEAD"); got != agentBranch {
		t.Fatalf("HEAD is on %q, want %q", got, agentBranch)
	}
	if got := runGit(t, co.Dir(), "rev-parse", "HEAD"); got != co.SHA() {
		t.Fatalf("HEAD = %s, want the base %s", got, co.SHA())
	}
	if co.Branch() != "main" {
		t.Fatalf("Branch() = %q, want the base branch main", co.Branch())
	}
}

func TestCheckout_startBranch_nameThatIsNotABranch_refused(t *testing.T) {
	t.Parallel()
	_, co := pilotCheckout(t)
	for _, name := range []string{"", "-f", "forja/../main", "forja/7.lock", "refs/heads/x y", "a//b"} {
		if err := co.StartBranch(context.Background(), name); err == nil {
			t.Errorf("StartBranch(%q) = nil, want a refusal", name)
		}
	}
	if got := runGit(t, co.Dir(), "rev-parse", "--abbrev-ref", "HEAD"); got != "main" {
		t.Fatalf("HEAD moved to %q", got)
	}
}

// The workspace's export of a depth-1 clone needs the base and nothing
// else, and the control plane's own depth-1 clone verifies it.
func TestBundle_ofDepthOneClone_singlePrerequisiteIsTheBase(t *testing.T) {
	t.Parallel()
	_, co := startedCheckout(t)
	ws := workspaceCopy(t, co)
	agentCommit(t, ws, map[string]string{"proof.txt": "one\n"}, "one")
	agentCommit(t, ws, map[string]string{"proof.txt": "two\n"}, "two")

	bundle := exportBundle(t, ws, co.SHA(), agentBranch)
	if got := prerequisites(t, bundle); len(got) != 1 || got[0] != co.SHA() {
		t.Fatalf("prerequisites = %q, want only the base %s", got, co.SHA())
	}
	runGit(t, co.Dir(), "bundle", "verify", "-q", bundle)
}

func TestCheckout_adoptBundle_treeAtTheAgentsHead(t *testing.T) {
	t.Parallel()
	_, co := startedCheckout(t)
	ws := workspaceCopy(t, co)
	agentCommit(t, ws, map[string]string{"proof.txt": "one\n"}, "one")
	head := agentCommit(t, ws, map[string]string{"docs/new.md": "new\n"}, "two")

	got, err := co.Adopt(context.Background(), exportBundle(t, ws, co.SHA(), agentBranch), agentBranch)
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if got != head {
		t.Fatalf("Adopt = %s, want the agent's head %s", got, head)
	}
	if on := runGit(t, co.Dir(), "rev-parse", "HEAD"); on != head {
		t.Fatalf("clone HEAD = %s, want %s", on, head)
	}
	if b := readFile(t, filepath.Join(co.Dir(), "docs", "new.md")); b != "new\n" {
		t.Fatalf("docs/new.md in Dir() = %q, want the agent's", b)
	}
	if status := runGit(t, co.Dir(), "status", "--porcelain"); status != "" {
		t.Fatalf("Dir() is not the head's tree:\n%s", status)
	}
}

// What the agent did to the contract reaches the tree the gates run on and
// nothing else: every read still answers for the base commit.
func TestCheckout_adoptBundleChangingContract_readsStillAnswerForBase(t *testing.T) {
	t.Parallel()
	repo, co := startedCheckout(t)
	baseBlob, err := co.BlobSHA(context.Background(), ".forja.yaml")
	if err != nil {
		t.Fatal(err)
	}
	ws := workspaceCopy(t, co)
	agentCommit(t, ws, map[string]string{".forja.yaml": agentContract}, "rewrite the contract")

	if _, err := co.Adopt(context.Background(), exportBundle(t, ws, co.SHA(), agentBranch), agentBranch); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	body, err := co.ReadFile(context.Background(), ".forja.yaml")
	if err != nil || string(body) != pilotFiles[".forja.yaml"] {
		t.Fatalf("ReadFile(.forja.yaml) = %q, %v; want the base's", body, err)
	}
	blob, err := co.BlobSHA(context.Background(), ".forja.yaml")
	if err != nil || blob != baseBlob || blob != repo.hashObject(pilotFiles[".forja.yaml"]) {
		t.Fatalf("BlobSHA = %s, %v; want the base blob %s", blob, err, baseBlob)
	}
	if onDisk := readFile(t, filepath.Join(co.Dir(), ".forja.yaml")); onDisk != agentContract {
		t.Fatalf(".forja.yaml in Dir() = %q, want the agent's", onDisk)
	}
}

// A hook in the control plane's clone never runs on an import: nothing of
// the agent's tree may execute on the control plane.
func TestCheckout_adopt_noHookRuns(t *testing.T) {
	t.Parallel()
	_, co := startedCheckout(t)
	ws := workspaceCopy(t, co)
	agentCommit(t, ws, map[string]string{"proof.txt": "one\n"}, "one")
	bundle := exportBundle(t, ws, co.SHA(), agentBranch)
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hook := "#!/bin/sh\ntouch '" + marker + "'\n"
	for _, name := range []string{"post-checkout", "reference-transaction", "post-merge"} {
		if err := os.WriteFile(filepath.Join(co.Dir(), ".git", "hooks", name), []byte(hook), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := co.Adopt(context.Background(), bundle, agentBranch); err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a hook ran during Adopt: %v", err)
	}
}

// adoptCase builds, from the started checkout, a bundle the control plane
// must refuse, and the branch it asks Adopt for.
type adoptCase struct {
	name   string
	bundle func(t *testing.T, repo *bareRepo, co *github.Checkout) string
	branch string
	want   error
}

func adoptRefusals() []adoptCase {
	return []adoptCase{
		{name: "branch at the base", branch: agentBranch, want: domain.ErrNoCommits, bundle: func(t *testing.T, repo *bareRepo, _ *github.Checkout) string {
			runGit(t, repo.work, "branch", agentBranch)
			path := filepath.Join(t.TempDir(), "full.bundle")
			runGit(t, repo.work, "bundle", "create", "-q", path, "refs/heads/"+agentBranch)
			return path
		}},
		{name: "history unrelated to the base", branch: agentBranch, bundle: func(t *testing.T, _ *bareRepo, co *github.Checkout) string {
			ws := workspaceCopy(t, co)
			runGit(t, ws, "checkout", "-q", "--orphan", "orphan")
			agentCommit(t, ws, map[string]string{"x": "x\n"}, "orphan")
			runGit(t, ws, "branch", "-f", agentBranch, "orphan")
			return exportBundle(t, ws, co.SHA(), agentBranch)
		}},
		{name: "bundle of another branch", branch: agentBranch, bundle: func(t *testing.T, _ *bareRepo, co *github.Checkout) string {
			ws := workspaceCopy(t, co)
			runGit(t, ws, "checkout", "-q", "-b", "forja/8")
			agentCommit(t, ws, map[string]string{"x": "x\n"}, "other")
			return exportBundle(t, ws, co.SHA(), "forja/8")
		}},
		{name: "not a bundle", branch: agentBranch, bundle: func(t *testing.T, _ *bareRepo, _ *github.Checkout) string {
			path := filepath.Join(t.TempDir(), "commits.bundle")
			if err := os.WriteFile(path, []byte("# v2 git bundle\nnot really\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{name: "branch name an option", branch: "--upload-pack=touch", bundle: func(t *testing.T, _ *bareRepo, co *github.Checkout) string {
			ws := workspaceCopy(t, co)
			agentCommit(t, ws, map[string]string{"x": "x\n"}, "one")
			return exportBundle(t, ws, co.SHA(), agentBranch)
		}},
	}
}

func TestCheckout_adopt_notTheAgentsWorkOnTheBase_refusedTreeUntouched(t *testing.T) {
	t.Parallel()
	for _, tt := range adoptRefusals() {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo, co := startedCheckout(t)
			bundle := tt.bundle(t, repo, co)

			head, err := co.Adopt(context.Background(), bundle, tt.branch)
			if err == nil {
				t.Fatalf("Adopt = %s, nil; want a refusal", head)
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("Adopt error = %v, want %v", err, tt.want)
			}
			if on := runGit(t, co.Dir(), "rev-parse", "HEAD"); on != co.SHA() {
				t.Fatalf("clone HEAD = %s after a refused Adopt, want the base %s", on, co.SHA())
			}
		})
	}
}
