package github_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/github"
)

// roundRepo is the pilot with a Forja branch an earlier run pushed: one
// agent commit on top of main that also rewrote the contract.
func roundRepo(t *testing.T) (repo *bareRepo, base, tip string) {
	t.Helper()
	repo = newBareRepo(t, "main")
	base = repo.commit(pilotFiles, "pilot")
	repo.branch(agentBranch)
	tip = repo.commit(map[string]string{"proof.txt": "first round\n", ".forja.yaml": agentContract}, "first round")
	return repo, base, tip
}

// roundCheckout clones main and checks the Forja branch out, closing the
// checkout when the test ends.
func roundCheckout(t *testing.T) (repo *bareRepo, co *github.Checkout, base, tip string) {
	t.Helper()
	repo, base, tip = roundRepo(t)
	cloner, _ := newCloner(t, repo)
	co, err := cloner.CloneBranch(context.Background(), fakeRepo, "main", agentBranch)
	if err != nil {
		t.Fatalf("CloneBranch: %v", err)
	}
	t.Cleanup(func() { _ = co.Close() })
	return repo, co, base, tip
}

// The tree the agent receives is the branch at its tip, on that branch;
// every read still answers for the base commit, whose contract the branch
// rewrote.
func TestCloner_cloneBranch_treeAtBranchTipReadsAtBase(t *testing.T) {
	t.Parallel()
	_, co, base, tip := roundCheckout(t)

	if co.SHA() != base || co.Start() != tip || co.Branch() != "main" {
		t.Fatalf("SHA %s Start %s Branch %q, want base %s, start at the tip %s, base branch main", co.SHA(), co.Start(), co.Branch(), base, tip)
	}
	if on := runGit(t, co.Dir(), "rev-parse", "HEAD"); on != tip {
		t.Fatalf("HEAD = %s, want the branch tip %s", on, tip)
	}
	if b := runGit(t, co.Dir(), "rev-parse", "--abbrev-ref", "HEAD"); b != agentBranch {
		t.Fatalf("HEAD is on %q, want %q", b, agentBranch)
	}
	if onDisk := readFile(t, filepath.Join(co.Dir(), "proof.txt")); onDisk != "first round\n" {
		t.Fatalf("proof.txt in Dir() = %q, want the tip's", onDisk)
	}
	body, err := co.ReadFile(context.Background(), ".forja.yaml")
	if err != nil || string(body) != pilotFiles[".forja.yaml"] {
		t.Fatalf("ReadFile(.forja.yaml) = %q, %v; want the base's, not the branch's", body, err)
	}
	if _, err := co.ReadFile(context.Background(), "proof.txt"); err == nil {
		t.Fatal("ReadFile(proof.txt) answered from the branch, want the base, which has none")
	}
}

func TestCloner_cloneBranch_branchMissing_errorDirRemoved(t *testing.T) {
	t.Parallel()
	repo, _, _ := roundRepo(t)
	cloner, dir := newCloner(t, repo)

	_, err := cloner.CloneBranch(context.Background(), fakeRepo, "main", "forja/8")
	if err == nil {
		t.Fatal("CloneBranch of a branch the remote does not have succeeded")
	}
	for _, want := range []string{"forja/8", fakeRepo} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	assertEmptyDir(t, dir)
}

// A branch that is not a branch name, or that is the base itself, is
// refused before a token is minted or git starts.
func TestCloner_cloneBranch_badBranch_refusedBeforeGit(t *testing.T) {
	t.Parallel()
	for _, branch := range []string{"", "-f", "--upload-pack=touch", "forja/../main", "main"} {
		bin, argsFile, _ := recordingGit(t)
		tokens := &countingTokens{value: cloneToken}
		dir := t.TempDir()
		cloner := &github.Cloner{Tokens: tokens, Dir: dir, Remote: "file:///nonexistent/", Git: bin}

		if _, err := cloner.CloneBranch(context.Background(), fakeRepo, "main", branch); err == nil {
			t.Errorf("CloneBranch(%q) succeeded", branch)
		}
		if n := len(tokens.mints()); n != 0 {
			t.Errorf("CloneBranch(%q) minted %d tokens", branch, n)
		}
		if _, err := os.Stat(argsFile); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("CloneBranch(%q) started git", branch)
		}
		assertEmptyDir(t, dir)
	}
}

// The credential reaches the clone and the fetch of the branch only
// through their environment: never an argument, never the clone's
// configuration, never the checkout.
func TestCloner_cloneBranch_tokenOnlyInEnv(t *testing.T) {
	t.Parallel()
	repo, _, _ := roundRepo(t)
	bin, argsFile, envFile := recordingGit(t)
	tokens := &countingTokens{value: cloneToken}
	cloner := &github.Cloner{Tokens: tokens, Dir: t.TempDir(), Remote: repo.remote(), Git: bin}

	co, err := cloner.CloneBranch(context.Background(), fakeRepo, "main", agentBranch)
	if err != nil {
		t.Fatalf("CloneBranch: %v", err)
	}
	t.Cleanup(func() { _ = co.Close() })

	args := readFile(t, argsFile)
	assertNoCredential(t, "git arguments", args, cloneToken)
	for _, want := range []string{"credential.helper=\n", "core.hooksPath=/dev/null\n", "--no-tags\n", "--no-recurse-submodules\n", "refs/heads/" + agentBranch} {
		if !strings.Contains(args, want) {
			t.Errorf("git arguments lack %q:\n%s", strings.TrimSpace(want), args)
		}
	}
	env := readFile(t, envFile)
	assertGitEnv(t, env, cloneToken)
	if n := strings.Count(env, "GIT_CONFIG_VALUE_0="); n != 2 {
		t.Errorf("the credential reached %d git invocations, want the clone and the fetch", n)
	}
	assertNoCredential(t, ".git/config of the checkout", readFile(t, filepath.Join(co.Dir(), ".git", "config")), cloneToken)
	assertEveryMint(t, tokens, github.ReadContents)
}
