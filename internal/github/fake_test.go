package github_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/github"
)

// The bodies below have the API's status and shape: the contents call (200
// with base64 content chunked in 60-char lines, and 404 "Not Found" for a
// missing file), the "No commit found" 404 for an unknown ref, and the
// branch and repository fields the reader uses. Values are the test's.

const (
	fakeOwner = "fuseone-io"
	fakeName  = "fuseone-forja"
	fakeRepo  = fakeOwner + "/" + fakeName
	fakeSHA   = "3d5656a3e223a7bc54b8e8f13086f562b84dc90c"
	// fakeBranch is deliberately not main: a reader that assumes the name
	// of the default branch instead of resolving it gets a 404 here.
	fakeBranch = "trunk"
)

// fakeGitHub serves the three endpoints ContractReader uses and enforces the
// API's own invariants: a bearer token that is not the one minted is 401, a
// ref that is not the branch commit does not exist, and a missing file is
// "Not Found".
type fakeGitHub struct {
	t        *testing.T
	token    string
	contract []byte
	// vanished makes the branch commit unknown to the contents endpoint,
	// as after a force push between the two calls.
	vanished bool
	// encoding is what the contents endpoint declares; anything but base64
	// comes with an empty content, as the API answers for a large file.
	encoding string
	server   *httptest.Server
}

func newFakeGitHub(t *testing.T, token string, contract []byte) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{t: t, token: token, contract: contract, encoding: "base64"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/{owner}/{name}", f.repository)
	mux.HandleFunc("GET /repos/{owner}/{name}/branches/{branch}", f.branch)
	mux.HandleFunc("GET /repos/{owner}/{name}/contents/{path...}", f.contents)
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeGitHub) URL() string { return f.server.URL }

// authorised rejects any request that does not carry the minted token or
// the JSON media type, as the API does.
func (f *fakeGitHub) authorised(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Accept") != "application/vnd.github+json" {
		f.t.Errorf("%s %s: Accept = %q", r.Method, r.URL, r.Header.Get("Accept"))
	}
	if r.PathValue("owner") != fakeOwner || r.PathValue("name") != fakeName {
		writeJSON(w, http.StatusNotFound, `{"message":"Not Found","documentation_url":"https://docs.github.com/rest","status":"404"}`)
		return false
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		writeJSON(w, http.StatusUnauthorized, `{"message":"Bad credentials","documentation_url":"https://docs.github.com/rest","status":"401"}`)
		return false
	}
	return true
}

func (f *fakeGitHub) repository(w http.ResponseWriter, r *http.Request) {
	if !f.authorised(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, fmt.Sprintf(`{"full_name":%q,"private":false,"default_branch":%q}`, fakeRepo, fakeBranch))
}

func (f *fakeGitHub) branch(w http.ResponseWriter, r *http.Request) {
	if !f.authorised(w, r) {
		return
	}
	if r.PathValue("branch") != fakeBranch {
		writeJSON(w, http.StatusNotFound, `{"message":"Branch not found","documentation_url":"https://docs.github.com/rest/branches/branches#get-a-branch","status":"404"}`)
		return
	}
	writeJSON(w, http.StatusOK, fmt.Sprintf(`{"name":%q,"commit":{"sha":%q,"url":"https://api.github.com/repos/%s/commits/%s"},"protected":false}`, fakeBranch, fakeSHA, fakeRepo, fakeSHA))
}

func (f *fakeGitHub) contents(w http.ResponseWriter, r *http.Request) {
	if !f.authorised(w, r) {
		return
	}
	ref := r.URL.Query().Get("ref")
	if ref != fakeSHA || f.vanished {
		writeJSON(w, http.StatusNotFound, fmt.Sprintf(`{"message":"No commit found for the ref %s","documentation_url":"https://docs.github.com/v3/repos/contents/","status":"404"}`, ref))
		return
	}
	if r.PathValue("path") != ".forja.yaml" || f.contract == nil {
		writeJSON(w, http.StatusNotFound, `{"message":"Not Found","documentation_url":"https://docs.github.com/rest/repos/contents#get-repository-content","status":"404"}`)
		return
	}
	content := chunkedBase64(f.contract)
	if f.encoding != "base64" {
		content = ""
	}
	body := map[string]any{
		"name": ".forja.yaml", "path": ".forja.yaml", "sha": "ea53aa91deb7ba2bb2695aa876dce4636adf9a97",
		"size": len(f.contract), "type": "file", "encoding": f.encoding,
		"content":      content,
		"url":          fmt.Sprintf("https://api.github.com/repos/%s/contents/.forja.yaml?ref=%s", fakeRepo, fakeSHA),
		"html_url":     fmt.Sprintf("https://github.com/%s/blob/%s/.forja.yaml", fakeRepo, fakeSHA),
		"git_url":      fmt.Sprintf("https://api.github.com/repos/%s/git/blobs/ea53aa91deb7ba2bb2695aa876dce4636adf9a97", fakeRepo),
		"download_url": fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/.forja.yaml", fakeRepo, fakeSHA),
		"_links":       map[string]string{"self": "", "git": "", "html": ""},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		f.t.Fatalf("marshal contents body: %v", err)
	}
	writeJSON(w, http.StatusOK, string(raw))
}

// chunkedBase64 encodes as the API does: 60-character lines, each ending
// in a newline.
func chunkedBase64(b []byte) string {
	enc := base64.StdEncoding.EncodeToString(b)
	var out strings.Builder
	for i := 0; i < len(enc); i += 60 {
		end := min(i+60, len(enc))
		out.WriteString(enc[i:end])
		out.WriteString("\n")
	}
	return out.String()
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// countingTokens is a TokenSource that records every mint: the repository
// and the permissions asked for.
type countingTokens struct {
	mu    sync.Mutex
	value string
	repos []string
	perms []github.Permissions
}

func (c *countingTokens) Token(_ context.Context, repo string, perms github.Permissions) (github.Token, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.repos = append(c.repos, repo)
	c.perms = append(c.perms, perms)
	return github.Token{Value: c.value}, nil
}

func (c *countingTokens) mints() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.repos...)
}

func (c *countingTokens) permissions() []github.Permissions {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]github.Permissions(nil), c.perms...)
}

// assertEveryMint checks that every token was minted with exactly the
// permissions given and nothing else.
func assertEveryMint(t *testing.T, tokens *countingTokens, want github.Permissions) {
	t.Helper()
	perms := tokens.permissions()
	if len(perms) == 0 {
		t.Fatal("no token was minted")
	}
	for i, got := range perms {
		if len(got) != len(want) {
			t.Fatalf("mint %d asked for %v, want exactly %v", i, got, want)
		}
		for scope, level := range want {
			if got[scope] != level {
				t.Fatalf("mint %d asked for %v, want %v", i, got, want)
			}
		}
	}
}
