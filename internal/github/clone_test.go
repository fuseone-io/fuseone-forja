package github_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/github"
)

const cloneToken = "ghs_clone_token_0123456789"

var pilotFiles = map[string]string{
	".forja.yaml":     "version: 1\nimage: golang:1.25\n",
	"CLAUDE.md":       "# Forja\n",
	"CONTRIBUTING.md": "# Contributing\n",
	"docs/b.md":       "b\n",
	"docs/a.md":       "a\n",
	"docs/sub/c.md":   "c\n",
	"docs/z.md":       "z\n",
}

// newCloner points a Cloner at the bare repository over file:// with its
// clone root in a fresh directory the test inspects afterwards.
func newCloner(t *testing.T, repo *bareRepo) (*github.Cloner, string) {
	t.Helper()
	dir := t.TempDir()
	cloner := &github.Cloner{Tokens: &countingTokens{value: cloneToken}, Dir: dir, Remote: repo.remote()}
	return cloner, dir
}

// cloneOf clones branch and closes the checkout when the test ends.
func cloneOf(t *testing.T, repo *bareRepo, branch string) *github.Checkout {
	t.Helper()
	cloner, _ := newCloner(t, repo)
	co, err := cloner.Clone(context.Background(), fakeRepo, branch)
	if err != nil {
		t.Fatalf("Clone(%q): %v", branch, err)
	}
	t.Cleanup(func() { _ = co.Close() })
	return co
}

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read clone root: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("clone root still holds %q", names)
	}
}

func TestCloner_branchGiven_checkoutAtThatBranchTip(t *testing.T) {
	t.Parallel()
	repo := newBareRepo(t, "main")
	repo.commit(pilotFiles, "pilot")
	repo.branch("feature/x")
	want := repo.commit(map[string]string{"CLAUDE.md": "# Forja on a branch\n"}, "branch commit")

	co := cloneOf(t, repo, "feature/x")
	if co.SHA() != want {
		t.Errorf("SHA() = %q, want the tip of feature/x %q", co.SHA(), want)
	}
	if co.SHA() == repo.revParse("main") {
		t.Errorf("SHA() is the tip of main, the branch asked for was ignored")
	}
	if co.Branch() != "feature/x" {
		t.Errorf("Branch() = %q, want feature/x", co.Branch())
	}
}

func TestCloner_noBranch_defaultBranchResolved(t *testing.T) {
	t.Parallel()
	repo := newBareRepo(t, fakeBranch)
	want := repo.commit(pilotFiles, "pilot")

	co := cloneOf(t, repo, "")
	if co.Branch() != fakeBranch {
		t.Errorf("Branch() = %q, want the default branch %q", co.Branch(), fakeBranch)
	}
	if co.SHA() != want {
		t.Errorf("SHA() = %q, want %q", co.SHA(), want)
	}
}

func TestCloner_close_removesDirectory(t *testing.T) {
	t.Parallel()
	repo := newBareRepo(t, "main")
	repo.commit(pilotFiles, "pilot")
	cloner, dir := newCloner(t, repo)

	co, err := cloner.Clone(context.Background(), fakeRepo, "main")
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("clone root holds %d entries while the checkout is open, want 1", len(entries))
	}
	if err := co.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertEmptyDir(t, dir)
}

func TestCloner_unknownBranch_errorNamesBranchAndDirectoryRemoved(t *testing.T) {
	t.Parallel()
	repo := newBareRepo(t, "main")
	repo.commit(pilotFiles, "pilot")
	cloner, dir := newCloner(t, repo)

	_, err := cloner.Clone(context.Background(), fakeRepo, "ghost")
	if err == nil {
		t.Fatal("Clone of an unknown branch succeeded")
	}
	for _, want := range []string{"ghost", fakeRepo} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	assertEmptyDir(t, dir)
}

// The remote never answers: cancelling the context must kill git and leave
// no directory behind.
func TestCloner_contextCancelled_gitKilledAndDirectoryRemoved(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	cloner := &github.Cloner{Tokens: &countingTokens{value: cloneToken}, Dir: dir, Remote: server.URL + "/"}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err := cloner.Clone(ctx, fakeRepo, "main")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Clone error = %v, want the context's deadline", err)
	}
	assertEmptyDir(t, dir)
}

func TestCloner_remoteRefsUnchangedAfterClone(t *testing.T) {
	t.Parallel()
	repo := newBareRepo(t, "main")
	repo.commit(pilotFiles, "pilot")
	before := repo.refs()

	cloneOf(t, repo, "main")
	if after := repo.refs(); after != before {
		t.Fatalf("refs changed by a clone:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestCloner_noTokenSourceOrMalformedRepository_rejectedBeforeGit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cloners := map[string]*github.Cloner{
		"no token source": {Dir: dir, Remote: "file:///nonexistent/"},
		"malformed repo":  {Tokens: &countingTokens{value: cloneToken}, Dir: dir, Remote: "file:///nonexistent/"},
	}
	repos := map[string]string{"no token source": fakeRepo, "malformed repo": "a/b/c"}
	for name, cloner := range cloners {
		if _, err := cloner.Clone(context.Background(), repos[name], ""); err == nil {
			t.Errorf("%s: Clone succeeded", name)
		}
	}
	assertEmptyDir(t, dir)
}

// basicAuth is the Authorization value GitHub's git endpoint accepts for an
// App or PAT token: basic with the user x-access-token.
func basicAuth(token string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))
}

// A recording stand-in for git writes its arguments and environment, then
// hands over to the real git so the clone still happens.
func recordingGit(t *testing.T) (bin, args, env string) {
	t.Helper()
	dir := t.TempDir()
	args = filepath.Join(dir, "args")
	env = filepath.Join(dir, "env")
	bin = filepath.Join(dir, "git")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> '" + args + "'\nenv >> '" + env + "'\nexec git \"$@\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write stand-in: %v", err)
	}
	return bin, args, env
}

func TestCloner_tokenInEnvironmentNotArgumentsNotConfig(t *testing.T) {
	t.Parallel()
	repo := newBareRepo(t, "main")
	repo.commit(pilotFiles, "pilot")
	bin, argsFile, envFile := recordingGit(t)
	cloner := &github.Cloner{Tokens: &countingTokens{value: cloneToken}, Dir: t.TempDir(), Remote: repo.remote(), Git: bin}

	co, err := cloner.Clone(context.Background(), fakeRepo, "main")
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	t.Cleanup(func() { _ = co.Close() })

	args := readFile(t, argsFile)
	assertNoCredential(t, "git arguments", args, cloneToken)
	if !strings.Contains(args, "credential.helper=\n") {
		t.Errorf("git arguments do not disable credential helpers:\n%s", args)
	}
	assertGitEnv(t, readFile(t, envFile), cloneToken)
	assertNoCredential(t, ".git/config of the checkout", readFile(t, filepath.Join(co.Dir(), ".git", "config")), cloneToken)
}

// assertNoCredential checks that text carries neither the raw token nor its
// basic-auth form.
func assertNoCredential(t *testing.T, what, text string, tokens ...string) {
	t.Helper()
	for _, token := range tokens {
		for _, secret := range []string{token, basicAuth(token)} {
			if strings.Contains(text, secret) {
				t.Errorf("%s carries the credential:\n%s", what, text)
			}
		}
	}
}

// assertGitEnv checks the environment the stand-in saw: the credential as
// an extraheader, and the settings that keep the operator's git out.
func assertGitEnv(t *testing.T, env, token string) {
	t.Helper()
	if !strings.Contains(env, "GIT_CONFIG_VALUE_0=Authorization: "+basicAuth(token)+"\n") {
		t.Errorf("GIT_CONFIG_VALUE_0 does not carry the basic x-access-token credential")
	}
	if !strings.Contains(env, "GIT_CONFIG_KEY_0=http.") || !strings.Contains(env, ".extraheader\n") {
		t.Errorf("GIT_CONFIG_KEY_0 is not an http extraheader")
	}
	for _, want := range []string{"GIT_TERMINAL_PROMPT=0\n", "GIT_CONFIG_NOSYSTEM=1\n", "GIT_CONFIG_GLOBAL=/dev/null\n"} {
		if !strings.Contains(env, want) {
			t.Errorf("git environment lacks %q", strings.TrimSpace(want))
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// gitHTTP serves the bare repositories under root through git's own smart
// HTTP backend, demanding the basic x-access-token credential first.
type gitHTTP struct {
	mu      sync.Mutex
	methods []string
	backend http.Handler
	want    string
}

func newGitHTTP(t *testing.T, root, token string) (*gitHTTP, *httptest.Server) {
	t.Helper()
	execPath := runGit(t, root, "--exec-path")
	g := &gitHTTP{want: basicAuth(token)}
	g.backend = &cgi.Handler{
		Path: filepath.Join(execPath, "git-http-backend"),
		Env: []string{
			"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1",
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		},
		InheritEnv: []string{"PATH"},
	}
	server := httptest.NewServer(g)
	t.Cleanup(server.Close)
	return g, server
}

func (g *gitHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.methods = append(g.methods, r.Method)
	g.mu.Unlock()
	if r.Header.Get("Authorization") != g.want {
		w.Header().Set("WWW-Authenticate", `Basic realm="forja"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	g.backend.ServeHTTP(w, r)
}

func TestCloner_httpRemote_basicHeaderSentAndRejectedTokenNotInError(t *testing.T) {
	t.Parallel()
	repo := newBareRepo(t, "main")
	want := repo.commit(pilotFiles, "pilot")
	_, server := newGitHTTP(t, repo.root, cloneToken)

	right := &github.Cloner{Tokens: &countingTokens{value: cloneToken}, Dir: t.TempDir(), Remote: server.URL + "/"}
	co, err := right.Clone(context.Background(), fakeRepo, "main")
	if err != nil {
		t.Fatalf("Clone with the right token: %v", err)
	}
	t.Cleanup(func() { _ = co.Close() })
	if co.SHA() != want {
		t.Errorf("SHA() = %q, want %q", co.SHA(), want)
	}

	dir := t.TempDir()
	wrong := &github.Cloner{Tokens: &countingTokens{value: "ghs_the_wrong_one"}, Dir: dir, Remote: server.URL + "/"}
	_, err = wrong.Clone(context.Background(), fakeRepo, "main")
	if err == nil {
		t.Fatal("Clone with a rejected token succeeded")
	}
	assertNoCredential(t, "error", err.Error(), cloneToken, "ghs_the_wrong_one")
	if !strings.Contains(err.Error(), fakeRepo) {
		t.Errorf("error %q does not name the repository", err)
	}
	assertEmptyDir(t, dir)
}
