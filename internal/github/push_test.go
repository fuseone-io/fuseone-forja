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

const pushToken = "ghs_push_token_9876543210"

// adoptedHead is the pilot's control-plane clone with one agent commit
// imported on agentBranch, as the control plane holds it before the push.
func adoptedHead(t *testing.T) (*bareRepo, *github.Checkout, string) {
	t.Helper()
	repo, co := startedCheckout(t)
	ws := workspaceCopy(t, co)
	agentCommit(t, ws, map[string]string{"proof.txt": "forja\n"}, "forja proof")
	head, err := co.Adopt(context.Background(), exportBundle(t, ws, co.SHA(), agentBranch), agentBranch)
	if err != nil {
		t.Fatalf("Adopt: %v", err)
	}
	return repo, co, head
}

func TestPusher_pushesHeadToForjaBranch_tokenOnlyInEnv(t *testing.T) {
	t.Parallel()
	repo, co, head := adoptedHead(t)
	mainBefore := repo.revParse("refs/heads/main")
	bin, argsFile, envFile := recordingGit(t)
	tokens := &countingTokens{value: pushToken}
	pusher := &github.Pusher{Tokens: tokens, Remote: repo.remote(), Git: bin}

	err := pusher.Push(context.Background(), github.PushRequest{Repository: fakeRepo, Dir: co.Dir(), Head: head, Branch: agentBranch})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if got := repo.revParse("refs/heads/" + agentBranch); got != head {
		t.Fatalf("remote %s = %s, want the head %s", agentBranch, got, head)
	}
	if got := repo.revParse("refs/heads/main"); got != mainBefore {
		t.Fatalf("remote main moved from %s to %s", mainBefore, got)
	}
	assertEveryMint(t, tokens, github.WriteContents)
	args := readFile(t, argsFile)
	assertNoCredential(t, "git arguments", args, pushToken)
	for _, want := range []string{"credential.helper=\n", "core.hooksPath=/dev/null\n", "--no-verify\n", head + ":refs/heads/" + agentBranch + "\n"} {
		if !strings.Contains(args, want) {
			t.Errorf("git arguments lack %q:\n%s", strings.TrimSpace(want), args)
		}
	}
	if strings.Contains(args, "--force") || strings.Contains(args, "\n+") {
		t.Errorf("the push is forced:\n%s", args)
	}
	assertGitEnv(t, readFile(t, envFile), pushToken)
	assertNoCredential(t, ".git/config of the clone", readFile(t, filepath.Join(co.Dir(), ".git", "config")), pushToken)
}

// Push in main is refused in code: only forja/<positive issue> is a
// destination, and anything else stops before a token is minted or git
// starts.
func TestPusher_destinationNotForjaBranch_refusedBeforeGit(t *testing.T) {
	t.Parallel()
	const head = "3d5656a3e223a7bc54b8e8f13086f562b84dc90c"
	tests := []struct{ name, branch, head string }{
		{name: "main", branch: "main", head: head},
		{name: "fully qualified main", branch: "refs/heads/main", head: head},
		{name: "issue zero", branch: "forja/0", head: head},
		{name: "issue with a leading zero", branch: "forja/07", head: head},
		{name: "not a number", branch: "forja/x", head: head},
		{name: "claim ref", branch: "refs/forja/claims/1", head: head},
		{name: "empty", branch: "", head: head},
		{name: "dot dot", branch: "forja/7/../../main", head: head},
		{name: "two refspecs", branch: "forja/7 main", head: head},
		{name: "head not a sha", branch: "forja/7", head: "main"},
		{name: "head forced", branch: "forja/7", head: "+" + head},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bin, argsFile, _ := recordingGit(t)
			tokens := &countingTokens{value: pushToken}
			pusher := &github.Pusher{Tokens: tokens, Remote: "file:///nonexistent/", Git: bin}

			err := pusher.Push(context.Background(), github.PushRequest{Repository: fakeRepo, Dir: t.TempDir(), Head: tt.head, Branch: tt.branch})
			if err == nil {
				t.Fatalf("Push to %q of %q = nil, want a refusal", tt.branch, tt.head)
			}
			if n := len(tokens.mints()); n != 0 {
				t.Fatalf("a refused push minted %d tokens", n)
			}
			if _, err := os.Stat(argsFile); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("git started for a refused push: %v", err)
			}
		})
	}
}

// A branch already on the remote at another commit is not overwritten:
// the push is never forced.
func TestPusher_branchExistsElsewhere_refusedRemoteUnchanged(t *testing.T) {
	t.Parallel()
	repo, co, head := adoptedHead(t)
	repo.branch(agentBranch)
	other := repo.commit(map[string]string{"other.txt": "someone else\n"}, "other")
	pusher := &github.Pusher{Tokens: &countingTokens{value: pushToken}, Remote: repo.remote()}

	err := pusher.Push(context.Background(), github.PushRequest{Repository: fakeRepo, Dir: co.Dir(), Head: head, Branch: agentBranch})
	if err == nil {
		t.Fatal("Push over another commit = nil, want a refusal")
	}
	if got := repo.revParse("refs/heads/" + agentBranch); got != other {
		t.Fatalf("remote %s = %s, want it left at %s", agentBranch, got, other)
	}
}

func TestPusher_gitEchoesTheHeader_errorWithheld(t *testing.T) {
	t.Parallel()
	bin := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\nprintf 'fatal: header %s rejected\\n' \"$GIT_CONFIG_VALUE_0\" >&2\nexit 128\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	pusher := &github.Pusher{Tokens: &countingTokens{value: pushToken}, Remote: "file:///nonexistent/", Git: bin}

	err := pusher.Push(context.Background(), github.PushRequest{Repository: fakeRepo, Dir: t.TempDir(), Head: "3d5656a3e223a7bc54b8e8f13086f562b84dc90c", Branch: agentBranch})
	if err == nil {
		t.Fatal("Push succeeded with a git that exits 128")
	}
	assertNoCredential(t, "error", err.Error(), pushToken)
	if !strings.Contains(err.Error(), fakeRepo) {
		t.Errorf("error %q does not name the repository", err)
	}
}
