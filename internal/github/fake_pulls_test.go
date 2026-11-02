package github_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
)

// The fake answers in the API's shapes:
//   - GET /pulls/{n} answers number, html_url, state, head {ref, sha, repo},
//     base {ref} and changed_files; head.repo is null, not an object without
//     full_name, once the head's repository is gone. body is null, never
//     "", for a pull request with no description.
//   - GET /pulls/{n}/files answers an array of diff entries with additions,
//     blob_url, changes, contents_url, deletions, filename, patch, raw_url,
//     sha and status; a rename adds previous_filename.
//   - per_page is capped at 100 without an error, and no more than 3000
//     files are ever listed whatever changed_files says: the page past the
//     cap is an empty array.
//   - An unknown number is 404 "Not Found".

// maxListedFiles is the API's cap on what the files endpoint lists.
const maxListedFiles = 3000

// fakeFile is one diff entry the fake lists.
type fakeFile struct {
	name     string
	previous string
	patch    string
}

// fakePulls serves one pull request and its files, enforcing the API's
// invariants above. headRepo nil is the API's null.
type fakePulls struct {
	t            *testing.T
	token        string
	number       int
	title, body  string
	headRef      string
	headSHA      string
	headRepo     *string
	baseRef      string
	changedFiles int
	files        []fakeFile
	// filesDown makes the files endpoint answer 500.
	filesDown bool
	server    *httptest.Server

	mu    sync.Mutex
	pages []string
}

func newFakePulls(t *testing.T, token string) *fakePulls {
	t.Helper()
	repo := fakeRepo
	f := &fakePulls{
		t: t, token: token, number: 57, title: "Write the proof file", body: "## What to build\n\nprivate-looking body",
		headRef: "forja/12", headSHA: deliveryHead, headRepo: &repo, baseRef: "main",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/{owner}/{name}/pulls/{number}", f.handle(f.pull))
	mux.HandleFunc("GET /repos/{owner}/{name}/pulls/{number}/files", f.handle(f.listFiles))
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			f.t.Errorf("%s %s: a reader wrote to the API", r.Method, r.URL)
			writeJSON(w, http.StatusMethodNotAllowed, `{"message":"Method Not Allowed"}`)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakePulls) URL() string { return f.server.URL }

// withFiles lists n modified files named f0.go, f1.go, ... and says so in
// changed_files.
func (f *fakePulls) withFiles(n int) {
	f.files = nil
	for i := range n {
		f.files = append(f.files, fakeFile{name: "f" + strconv.Itoa(i) + ".go", patch: "@@ -1 +1 @@\n-a\n+b"})
	}
	f.changedFiles = n
}

func (f *fakePulls) handle(endpoint http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			f.t.Errorf("%s %s: Accept = %q", r.Method, r.URL, r.Header.Get("Accept"))
		}
		number, err := strconv.Atoi(r.PathValue("number"))
		if r.PathValue("owner") != fakeOwner || r.PathValue("name") != fakeName || err != nil || number != f.number {
			writeJSON(w, http.StatusNotFound, `{"message":"Not Found","documentation_url":"https://docs.github.com/rest/pulls/pulls#get-a-pull-request","status":"404"}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+f.token {
			writeJSON(w, http.StatusUnauthorized, `{"message":"Bad credentials","documentation_url":"https://docs.github.com/rest","status":"401"}`)
			return
		}
		endpoint(w, r)
	}
}

func (f *fakePulls) pull(w http.ResponseWriter, _ *http.Request) {
	var repo any
	if f.headRepo != nil {
		repo = map[string]any{"full_name": *f.headRepo, "private": false}
	}
	var body any
	if f.body != "" {
		body = f.body
	}
	f.writeBody(w, map[string]any{
		"number": f.number, "html_url": f.html(), "state": "open", "title": f.title, "body": body,
		"head":          map[string]any{"ref": f.headRef, "sha": f.headSHA, "repo": repo},
		"base":          map[string]any{"ref": f.baseRef, "sha": fakeSHA},
		"changed_files": f.changedFiles, "merged": false, "draft": false,
	})
}

func (f *fakePulls) html() string {
	return fmt.Sprintf("https://github.com/%s/pull/%d", fakeRepo, f.number)
}

func (f *fakePulls) listFiles(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.pages = append(f.pages, r.URL.RawQuery)
	f.mu.Unlock()
	if f.filesDown {
		writeJSON(w, http.StatusInternalServerError, `{"message":"Server Error"}`)
		return
	}
	perPage, page := queryInt(r, "per_page", 30), queryInt(r, "page", 1)
	perPage = min(max(perPage, 1), 100)
	listed := f.files[:min(len(f.files), maxListedFiles)]
	start := min((page-1)*perPage, len(listed))
	entries := make([]map[string]any, 0, perPage)
	for _, file := range listed[start:min(start+perPage, len(listed))] {
		entries = append(entries, f.entry(file))
	}
	f.writeBody(w, entries)
}

func (f *fakePulls) entry(file fakeFile) map[string]any {
	e := map[string]any{
		"sha": "bbcd538c8e72b8c175046e27cc8f907076331401", "filename": file.name, "status": "modified",
		"additions": 1, "deletions": 1, "changes": 2, "patch": file.patch,
		"blob_url":     "https://github.com/" + fakeRepo + "/blob/" + f.headSHA + "/" + file.name,
		"raw_url":      "https://github.com/" + fakeRepo + "/raw/" + f.headSHA + "/" + file.name,
		"contents_url": "https://api.github.com/repos/" + fakeRepo + "/contents/" + file.name + "?ref=" + f.headSHA,
	}
	if file.previous != "" {
		e["status"], e["previous_filename"] = "renamed", file.previous
	}
	return e
}

func (f *fakePulls) writeBody(w http.ResponseWriter, body any) {
	raw, err := json.Marshal(body)
	if err != nil {
		f.t.Fatalf("marshal: %v", err)
	}
	writeJSON(w, http.StatusOK, string(raw))
}

func (f *fakePulls) filePages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.pages...)
}

func queryInt(r *http.Request, key string, fallback int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil {
		return fallback
	}
	return n
}
