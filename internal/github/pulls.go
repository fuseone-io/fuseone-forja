package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// PullRequests opens pull requests through the API with a token minted to
// write pull requests for each call. Errors carry the status and the API's
// message, never the body that was sent.
type PullRequests struct {
	Tokens TokenSource
	// HTTP is the client used against BaseURL; nil means a client with a
	// default timeout.
	HTTP *http.Client
	// BaseURL is the API root; empty means DefaultBaseURL.
	BaseURL string
}

// PullRequestRequest is one pull request from Head into Base of
// Repository, both branch names of that repository.
type PullRequestRequest struct {
	Repository string
	Title      string
	Head       string
	Base       string
	Body       string
}

type pullResponse struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
}

// Open creates the pull request and returns its number and page. A
// refusal, such as a head that already has one open, is a *StatusError.
func (p *PullRequests) Open(ctx context.Context, req PullRequestRequest) (domain.PullRequest, error) {
	base, err := repoPath(req.Repository)
	if err != nil {
		return domain.PullRequest{}, err
	}
	if req.Head == "" || req.Base == "" || req.Title == "" {
		return domain.PullRequest{}, errors.New("github: a pull request needs a title, a head and a base")
	}
	api, err := mintClient(ctx, scopedClient{tokens: p.Tokens, repo: req.Repository, perms: WritePullRequests, http: p.HTTP, base: p.BaseURL})
	if err != nil {
		return domain.PullRequest{}, err
	}
	body, err := json.Marshal(struct {
		Title string `json:"title"`
		Head  string `json:"head"`
		Base  string `json:"base"`
		Body  string `json:"body"`
	}{Title: req.Title, Head: req.Head, Base: req.Base, Body: req.Body})
	if err != nil {
		return domain.PullRequest{}, fmt.Errorf("github: open pull request on %s: %w", req.Repository, err)
	}
	var res pullResponse
	if err := api.do(ctx, apiCall{path: base + "/pulls", body: body, want: http.StatusCreated}, &res); err != nil {
		return domain.PullRequest{}, fmt.Errorf("github: open pull request %s into %s on %s: %w", req.Head, req.Base, req.Repository, err)
	}
	return domain.PullRequest{Number: res.Number, URL: res.HTMLURL}, nil
}

// scopedClient is what mintClient needs to build one call's client.
type scopedClient struct {
	tokens TokenSource
	repo   string
	perms  Permissions
	http   *http.Client
	base   string
}

// mintClient mints a token for the repository with the call's permissions
// and returns a client that carries it.
func mintClient(ctx context.Context, r scopedClient) (*apiClient, error) {
	if r.tokens == nil {
		return nil, errors.New("github: no token source")
	}
	tok, err := r.tokens.Token(ctx, r.repo, r.perms)
	if err != nil {
		return nil, fmt.Errorf("github: %s: %w", r.repo, err)
	}
	return newAPIClient(r.http, r.base, tok.Value), nil
}

// repoPath is the API path of the repository, "/repos/owner/name".
func repoPath(repo string) (string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return "", err
	}
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name), nil
}
