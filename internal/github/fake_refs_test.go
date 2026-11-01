package github_test

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeGit serves the Git Data endpoints Refs uses and enforces the API's
// invariants: a ref that exists cannot be created, an update that is not a
// fast-forward of the current value is refused, a missing ref cannot be
// deleted, and contents are read at any commit, on a branch or not. Status
// codes are the REST reference's; the GET ref body and its 404 copy the
// API's. The error message texts are not what Refs decides on.
type fakeGit struct {
	t     *testing.T
	token string

	mu       sync.Mutex
	refs     map[string]string
	commits  map[string]fakeCommit
	trees    map[string]map[string]string
	blobs    map[string][]byte
	requests []string
	// commitBodies are the POST /git/commits payloads, in order.
	commitBodies []fakeCommitRequest
	server       *httptest.Server
}

type fakeCommit struct {
	Tree    string
	Parents []string
}

type fakeCommitRequest struct {
	Message string   `json:"message"`
	Tree    string   `json:"tree"`
	Parents []string `json:"parents"`
}

func newFakeGit(t *testing.T, token string) *fakeGit {
	t.Helper()
	f := &fakeGit{
		t: t, token: token,
		refs: map[string]string{}, commits: map[string]fakeCommit{},
		trees: map[string]map[string]string{}, blobs: map[string][]byte{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/{owner}/{name}/git/ref/{ref...}", f.handle(f.getRef))
	mux.HandleFunc("POST /repos/{owner}/{name}/git/refs", f.handle(f.createRef))
	mux.HandleFunc("PATCH /repos/{owner}/{name}/git/refs/{ref...}", f.handle(f.updateRef))
	mux.HandleFunc("DELETE /repos/{owner}/{name}/git/refs/{ref...}", f.handle(f.deleteRef))
	mux.HandleFunc("POST /repos/{owner}/{name}/git/blobs", f.handle(f.createBlob))
	mux.HandleFunc("POST /repos/{owner}/{name}/git/trees", f.handle(f.createTree))
	mux.HandleFunc("POST /repos/{owner}/{name}/git/commits", f.handle(f.createCommit))
	mux.HandleFunc("GET /repos/{owner}/{name}/contents/{path...}", f.handle(f.contents))
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeGit) URL() string { return f.server.URL }

// handle checks auth and the media type, records the request and runs the
// endpoint under the lock.
func (f *fakeGit) handle(endpoint func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			f.t.Errorf("%s %s: Accept = %q", r.Method, r.URL, r.Header.Get("Accept"))
		}
		if r.PathValue("owner") != fakeOwner || r.PathValue("name") != fakeName {
			writeJSON(w, http.StatusNotFound, `{"message":"Not Found","documentation_url":"https://docs.github.com/rest","status":"404"}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+f.token {
			writeJSON(w, http.StatusUnauthorized, `{"message":"Bad credentials","documentation_url":"https://docs.github.com/rest","status":"401"}`)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		endpoint(w, r)
	}
}

func (f *fakeGit) getRef(w http.ResponseWriter, r *http.Request) {
	ref := "refs/" + r.PathValue("ref")
	sha, ok := f.refs[ref]
	if !ok {
		writeJSON(w, http.StatusNotFound, `{"message":"Not Found","documentation_url":"https://docs.github.com/rest/git/refs#get-a-reference","status":"404"}`)
		return
	}
	writeJSON(w, http.StatusOK, f.refBody(ref, sha))
}

func (f *fakeGit) refBody(ref, sha string) string {
	return fmt.Sprintf(`{"ref":%q,"node_id":"REF_kwDOVekxTK9yZWZzL2hlYWRzL21haW4","url":"https://api.github.com/repos/%s/git/%s","object":{"sha":%q,"type":"commit","url":"https://api.github.com/repos/%s/git/commits/%s"}}`,
		ref, fakeRepo, ref, sha, fakeRepo, sha)
}

func (f *fakeGit) createRef(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !strings.HasPrefix(body.Ref, "refs/") {
		writeJSON(w, http.StatusUnprocessableEntity, `{"message":"Reference name must be a valid fully qualified reference","status":"422"}`)
		return
	}
	if _, exists := f.refs[body.Ref]; exists {
		writeJSON(w, http.StatusUnprocessableEntity, `{"message":"Reference already exists","documentation_url":"https://docs.github.com/rest/git/refs#create-a-reference","status":"422"}`)
		return
	}
	if _, known := f.commits[body.SHA]; !known {
		writeJSON(w, http.StatusUnprocessableEntity, `{"message":"Object does not exist","documentation_url":"https://docs.github.com/rest/git/refs#create-a-reference","status":"422"}`)
		return
	}
	f.refs[body.Ref] = body.SHA
	writeJSON(w, http.StatusCreated, f.refBody(body.Ref, body.SHA))
}

func (f *fakeGit) updateRef(w http.ResponseWriter, r *http.Request) {
	ref := "refs/" + r.PathValue("ref")
	var body struct {
		SHA   string `json:"sha"`
		Force bool   `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, `{"message":"Validation Failed","status":"422"}`)
		return
	}
	if body.Force {
		f.t.Errorf("PATCH %s: force = true; the control plane never forces a ref", ref)
	}
	current, ok := f.refs[ref]
	if !ok {
		writeJSON(w, http.StatusUnprocessableEntity, `{"message":"Reference does not exist","documentation_url":"https://docs.github.com/rest/git/refs#update-a-reference","status":"422"}`)
		return
	}
	if !f.descends(body.SHA, current) {
		writeJSON(w, http.StatusUnprocessableEntity, `{"message":"Update is not a fast forward","documentation_url":"https://docs.github.com/rest/git/refs#update-a-reference","status":"422"}`)
		return
	}
	f.refs[ref] = body.SHA
	writeJSON(w, http.StatusOK, f.refBody(ref, body.SHA))
}

// descends reports whether ancestor is sha or one of its ancestors.
func (f *fakeGit) descends(sha, ancestor string) bool {
	for queue := []string{sha}; len(queue) > 0; queue = queue[1:] {
		if queue[0] == ancestor {
			return true
		}
		queue = append(queue, f.commits[queue[0]].Parents...)
	}
	return false
}

func (f *fakeGit) deleteRef(w http.ResponseWriter, r *http.Request) {
	ref := "refs/" + r.PathValue("ref")
	if _, ok := f.refs[ref]; !ok {
		writeJSON(w, http.StatusUnprocessableEntity, `{"message":"Reference does not exist","documentation_url":"https://docs.github.com/rest/git/refs#delete-a-reference","status":"422"}`)
		return
	}
	delete(f.refs, ref)
	w.WriteHeader(http.StatusNoContent)
}

func (f *fakeGit) createBlob(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Encoding != "base64" {
		writeJSON(w, http.StatusUnprocessableEntity, `{"message":"Validation Failed","status":"422"}`)
		return
	}
	raw, err := base64.StdEncoding.DecodeString(body.Content)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, `{"message":"content is not valid Base64","status":"422"}`)
		return
	}
	sha := fakeSHAOf("blob", string(raw))
	f.blobs[sha] = raw
	writeJSON(w, http.StatusCreated, fmt.Sprintf(`{"url":"https://api.github.com/repos/%s/git/blobs/%s","sha":%q}`, fakeRepo, sha, sha))
}

func (f *fakeGit) createTree(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Tree []struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"tree"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Tree) == 0 {
		writeJSON(w, http.StatusUnprocessableEntity, `{"message":"Validation Failed","status":"422"}`)
		return
	}
	entries := map[string]string{}
	for _, e := range body.Tree {
		if e.Mode != "100644" || e.Type != "blob" {
			f.t.Errorf("tree entry %+v: want a 100644 blob", e)
		}
		if _, known := f.blobs[e.SHA]; !known {
			writeJSON(w, http.StatusUnprocessableEntity, fmt.Sprintf(`{"message":"tree.sha %s is not a valid blob","status":"422"}`, e.SHA))
			return
		}
		entries[e.Path] = e.SHA
	}
	sha := fakeSHAOf("tree", fmt.Sprint(entries))
	f.trees[sha] = entries
	writeJSON(w, http.StatusCreated, fmt.Sprintf(`{"sha":%q,"url":"https://api.github.com/repos/%s/git/trees/%s","tree":[],"truncated":false}`, sha, fakeRepo, sha))
}

func (f *fakeGit) createCommit(w http.ResponseWriter, r *http.Request) {
	var body fakeCommitRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, `{"message":"Validation Failed","status":"422"}`)
		return
	}
	if _, known := f.trees[body.Tree]; !known {
		writeJSON(w, http.StatusUnprocessableEntity, `{"message":"Tree SHA does not exist","status":"422"}`)
		return
	}
	for _, p := range body.Parents {
		if _, known := f.commits[p]; !known {
			writeJSON(w, http.StatusUnprocessableEntity, `{"message":"Parent SHA does not exist or is not a commit object","status":"422"}`)
			return
		}
	}
	f.commitBodies = append(f.commitBodies, body)
	sha := f.addCommit(body.Tree, body.Parents, body.Message)
	writeJSON(w, http.StatusCreated, fmt.Sprintf(`{"sha":%q,"node_id":"C_kwDOVekxTNoAKA","url":"https://api.github.com/repos/%s/git/commits/%s","message":%q,"tree":{"sha":%q},"parents":[]}`, sha, fakeRepo, sha, body.Message, body.Tree))
}

func (f *fakeGit) addCommit(tree string, parents []string, message string) string {
	if parents == nil {
		parents = []string{}
	}
	sha := fakeSHAOf("commit", tree+strings.Join(parents, ",")+message)
	f.commits[sha] = fakeCommit{Tree: tree, Parents: parents}
	return sha
}

func (f *fakeGit) contents(w http.ResponseWriter, r *http.Request) {
	ref := r.URL.Query().Get("ref")
	commit, ok := f.commits[ref]
	if !ok {
		writeJSON(w, http.StatusNotFound, fmt.Sprintf(`{"message":"No commit found for the ref %s","documentation_url":"https://docs.github.com/v3/repos/contents/","status":"404"}`, ref))
		return
	}
	path := r.PathValue("path")
	blobSHA, ok := f.trees[commit.Tree][path]
	if !ok {
		writeJSON(w, http.StatusNotFound, `{"message":"Not Found","documentation_url":"https://docs.github.com/rest/repos/contents#get-repository-content","status":"404"}`)
		return
	}
	raw := f.blobs[blobSHA]
	body := map[string]any{
		"name": path, "path": path, "sha": blobSHA, "size": len(raw), "type": "file",
		"encoding": "base64", "content": chunkedBase64(raw),
		"url":      fmt.Sprintf("https://api.github.com/repos/%s/contents/%s?ref=%s", fakeRepo, path, ref),
		"git_url":  fmt.Sprintf("https://api.github.com/repos/%s/git/blobs/%s", fakeRepo, blobSHA),
		"html_url": fmt.Sprintf("https://github.com/%s/blob/%s/%s", fakeRepo, ref, path),
		"_links":   map[string]string{"self": "", "git": "", "html": ""},
	}
	rawBody, err := json.Marshal(body)
	if err != nil {
		f.t.Fatalf("marshal contents body: %v", err)
	}
	writeJSON(w, http.StatusOK, string(rawBody))
}

// seed stores a commit holding files, as a child of parent when parent is
// not empty, and points ref at it when ref is not empty. It returns the
// commit's sha.
func (f *fakeGit) seed(ref, parent string, files map[string][]byte) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	entries := map[string]string{}
	for path, raw := range files {
		sha := fakeSHAOf("blob", string(raw))
		f.blobs[sha] = raw
		entries[path] = sha
	}
	tree := fakeSHAOf("tree", fmt.Sprint(entries))
	f.trees[tree] = entries
	var parents []string
	if parent != "" {
		parents = []string{parent}
	}
	sha := f.addCommit(tree, parents, "seed "+ref)
	if ref != "" {
		f.refs[ref] = sha
	}
	return sha
}

func (f *fakeGit) refSHA(ref string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sha, ok := f.refs[ref]
	return sha, ok
}

func (f *fakeGit) parentsOf(sha string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.commits[sha].Parents...)
}

func (f *fakeGit) requestLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...)
}

func (f *fakeGit) commitRequests() []fakeCommitRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCommitRequest(nil), f.commitBodies...)
}

// fakeSHAOf is a 40-hex content address, as Git's.
func fakeSHAOf(kind, content string) string {
	sum := sha1.Sum([]byte(kind + "\x00" + content))
	return hex.EncodeToString(sum[:])
}
