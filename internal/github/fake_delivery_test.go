package github_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeDelivery serves the two endpoints a green run writes to and
// enforces the API's invariants: a bearer token that is not the minted one
// is 401, a pull request needs a head that exists and a base, one head has
// at most one open pull request, and a status needs a known commit and one
// of the four states. Every refusal is 422 with the validation-error shape
// of the REST description: {message, documentation_url, errors: [{code,
// ...}]}.
//
// The 201 bodies copy measured shapes: the pull request's number,
// html_url, head and base as `gh api repos/fuseone-io/fuseone-forja/pulls/38`
// answered, and the status's fields as the REST description's example has
// them. The duplicate-head message text was not measured; nothing decides
// on it.
//
// The pull request listing copies what `gh api
// 'repos/fuseone-io/fuseone-forja/pulls?state=all&head=fuseone-io:<branch>'`
// answered: an array of {number, html_url, state, user, head {ref, sha,
// label, repo {full_name}}, base {ref}}; state defaults to open; a head of
// another owner matches nothing; and a head without the owner: prefix is
// ignored without an error, so every pull request comes back. The comment
// is the REST description's 201 with the id and html_url an existing PR
// comment of the repository has; GET /user is the {login, id, type} the gh
// token's user answered.
type fakeDelivery struct {
	t     *testing.T
	token string
	// branches are the heads that exist; commits the SHAs a status may
	// name; listed the pull requests the listing knows; login the user GET
	// /user names.
	branches map[string]bool
	commits  map[string]bool
	listed   []fakePull
	login    string

	mu       sync.Mutex
	nextPR   int
	open     map[string]int
	pulls    []map[string]any
	statuses []fakeStatus
	queries  []url.Values
	comments []fakeComment
	server   *httptest.Server
}

// fakePull is one pull request as the listing reports it.
type fakePull struct {
	number   int
	state    string
	head     string
	author   string
	base     string
	headRepo string
}

type fakeComment struct {
	Number int
	Body   map[string]any
}

type fakeStatus struct {
	SHA  string
	Body map[string]any
}

func newFakeDelivery(t *testing.T, token string) *fakeDelivery {
	t.Helper()
	f := &fakeDelivery{t: t, token: token, branches: map[string]bool{"main": true}, commits: map[string]bool{}, nextPR: 41, open: map[string]int{}, login: "phbpx"}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/{owner}/{name}/pulls", f.handle(f.createPull))
	mux.HandleFunc("GET /repos/{owner}/{name}/pulls", f.handleGet(f.listPulls))
	mux.HandleFunc("POST /repos/{owner}/{name}/issues/{number}/comments", f.handle(f.createComment))
	mux.HandleFunc("POST /repos/{owner}/{name}/statuses/{sha}", f.handle(f.createStatus))
	mux.HandleFunc("GET /user", f.user)
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeDelivery) URL() string { return f.server.URL }

// authorised answers 404 for another repository and 401 for a token that
// is not the minted one, as the API does.
func (f *fakeDelivery) authorised(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Accept") != "application/vnd.github+json" {
		f.t.Errorf("%s %s: Accept %q", r.Method, r.URL, r.Header.Get("Accept"))
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

func (f *fakeDelivery) handleGet(endpoint func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !f.authorised(w, r) {
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		endpoint(w, r)
	}
}

func (f *fakeDelivery) handle(endpoint func(w http.ResponseWriter, r *http.Request, body map[string]any)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			f.t.Errorf("%s %s: Content-Type %q", r.Method, r.URL, r.Header.Get("Content-Type"))
		}
		if !f.authorised(w, r) {
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, `{"message":"Problems parsing JSON","status":"400"}`)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		endpoint(w, r, body)
	}
}

func validationFailed(w http.ResponseWriter, resource, field, code, message string) {
	writeJSON(w, http.StatusUnprocessableEntity, fmt.Sprintf(
		`{"message":"Validation Failed","errors":[{"resource":%q,"field":%q,"code":%q,"message":%q}],"documentation_url":"https://docs.github.com/rest","status":"422"}`,
		resource, field, code, message))
}

func (f *fakeDelivery) createPull(w http.ResponseWriter, _ *http.Request, body map[string]any) {
	head, _ := body["head"].(string)
	base, _ := body["base"].(string)
	switch {
	case head == "" || base == "":
		validationFailed(w, "PullRequest", "head", "missing_field", "")
		return
	case !f.branches[head]:
		validationFailed(w, "PullRequest", "head", "invalid", "")
		return
	case !f.branches[base]:
		validationFailed(w, "PullRequest", "base", "invalid", "")
		return
	case f.open[head] != 0:
		validationFailed(w, "PullRequest", "", "custom", "A pull request already exists for "+fakeOwner+":"+head+".")
		return
	}
	number := f.nextPR
	f.nextPR++
	f.open[head] = number
	f.pulls = append(f.pulls, body)
	html := fmt.Sprintf("https://github.com/%s/pull/%d", fakeRepo, number)
	w.Header().Set("Location", fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d", fakeRepo, number))
	writeJSON(w, http.StatusCreated, fmt.Sprintf(
		`{"number":%d,"html_url":%q,"head":{"ref":%q,"sha":"47882cf0000000000000000000000000000000aa"},"base":{"ref":%q},"user":{"login":"phbpx","type":"User"},"state":"open","draft":false}`,
		number, html, head, base))
}

var statusStates = map[string]bool{"error": true, "failure": true, "pending": true, "success": true}

func (f *fakeDelivery) createStatus(w http.ResponseWriter, r *http.Request, body map[string]any) {
	sha := r.PathValue("sha")
	state, _ := body["state"].(string)
	if !f.commits[sha] {
		validationFailed(w, "Status", "sha", "invalid", "No commit found for SHA: "+sha)
		return
	}
	if !statusStates[state] {
		validationFailed(w, "Status", "state", "invalid", "")
		return
	}
	f.statuses = append(f.statuses, fakeStatus{SHA: sha, Body: body})
	raw, err := json.Marshal(map[string]any{
		"url": fmt.Sprintf("https://api.github.com/repos/%s/statuses/%s", fakeRepo, sha), "avatar_url": "https://github.com/images/error/hubot_happy.gif",
		"id": 1, "node_id": "MDY6U3RhdHVzMQ==", "state": state, "description": body["description"], "target_url": body["target_url"],
		"context": body["context"], "created_at": "2012-07-20T01:19:13Z", "updated_at": "2012-07-20T01:19:13Z",
		"creator": map[string]any{"login": "phbpx", "type": "User"},
	})
	if err != nil {
		f.t.Fatalf("marshal status: %v", err)
	}
	writeJSON(w, http.StatusCreated, string(raw))
}

// listPulls filters as the measured listing does: by state, open when
// absent, and by an owner:branch head, which a head without the prefix
// does not filter at all.
func (f *fakeDelivery) listPulls(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.queries = append(f.queries, q)
	state := q.Get("state")
	if state == "" {
		state = "open"
	}
	owner, ref, filtered := strings.Cut(q.Get("head"), ":")
	out := []map[string]any{}
	for _, p := range f.listed {
		if (state != "all" && p.state != state) || (filtered && (owner != fakeOwner || p.head != ref)) {
			continue
		}
		out = append(out, map[string]any{
			"number": p.number, "html_url": fmt.Sprintf("https://github.com/%s/pull/%d", fakeRepo, p.number), "state": p.state,
			"user": map[string]any{"login": p.author, "type": "User"},
			"head": map[string]any{
				"ref": p.head, "sha": "1f1d9cbdf3fabfb76ac8e38128dd835a05d7512b", "label": fakeOwner + ":" + p.head,
				"repo": map[string]any{"full_name": p.headRepo}, "user": map[string]any{"login": fakeOwner},
			},
			"base": map[string]any{"ref": p.base},
		})
	}
	raw, err := json.Marshal(out)
	if err != nil {
		f.t.Fatalf("marshal pulls: %v", err)
	}
	writeJSON(w, http.StatusOK, string(raw))
}

// createComment comments on a pull request the listing knows; another
// number is "Not Found", an empty body a validation failure.
func (f *fakeDelivery) createComment(w http.ResponseWriter, r *http.Request, body map[string]any) {
	number, err := strconv.Atoi(r.PathValue("number"))
	known := false
	for _, p := range f.listed {
		known = known || (err == nil && p.number == number)
	}
	if !known {
		writeJSON(w, http.StatusNotFound, `{"message":"Not Found","documentation_url":"https://docs.github.com/rest/issues/comments#create-an-issue-comment","status":"404"}`)
		return
	}
	if text, _ := body["body"].(string); text == "" {
		validationFailed(w, "IssueComment", "body", "missing_field", "")
		return
	}
	f.comments = append(f.comments, fakeComment{Number: number, Body: body})
	id := 6579424187 + len(f.comments)
	writeJSON(w, http.StatusCreated, fmt.Sprintf(
		`{"id":%d,"html_url":"https://github.com/%s/pull/%d#issuecomment-%d","issue_url":"https://api.github.com/repos/%s/issues/%d","user":{"login":%q,"type":"User"}}`,
		id, fakeRepo, number, id, fakeRepo, number, f.login))
}

// user answers GET /user for the configured token only.
func (f *fakeDelivery) user(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		writeJSON(w, http.StatusUnauthorized, `{"message":"Bad credentials","documentation_url":"https://docs.github.com/rest","status":"401"}`)
		return
	}
	writeJSON(w, http.StatusOK, fmt.Sprintf(`{"login":%q,"id":3296831,"type":"User"}`, f.login))
}

func (f *fakeDelivery) listQueries() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.queries...)
}

func (f *fakeDelivery) commentCalls() []fakeComment {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeComment(nil), f.comments...)
}

func (f *fakeDelivery) pullBodies() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.pulls...)
}

func (f *fakeDelivery) statusCalls() []fakeStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeStatus(nil), f.statuses...)
}
