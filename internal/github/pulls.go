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

// Sentinels for the pull request a branch is asked for.
var (
	ErrNoPullRequest    = errors.New("no open pull request")
	ErrManyPullRequests = errors.New("more than one open pull request")
)

// listedPull is the subset of a listed pull request Find reads. The head's
// repository is null when the fork it lived in was deleted.
type listedPull struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref  string `json:"ref"`
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// Find returns the one open pull request whose head is branch of the
// repository's owner, with who opened it, the branch it targets and the
// repository its head lives in, read with a token that only reads pull
// requests. The head is always sent as owner:branch: without the owner
// the API ignores the filter. None wraps ErrNoPullRequest, more than one
// ErrManyPullRequests.
func (p *PullRequests) Find(ctx context.Context, repo, branch string) (domain.PullRequest, error) {
	base, err := repoPath(repo)
	if err != nil {
		return domain.PullRequest{}, err
	}
	if err := validBranch(branch); err != nil {
		return domain.PullRequest{}, fmt.Errorf("github: find pull request: %w", err)
	}
	owner, _, _ := splitRepo(repo)
	api, err := mintClient(ctx, scopedClient{tokens: p.Tokens, repo: repo, perms: ReadPullRequests, http: p.HTTP, base: p.BaseURL})
	if err != nil {
		return domain.PullRequest{}, err
	}
	// Two answers are enough to tell one pull request from many.
	query := url.Values{"state": {"open"}, "head": {owner + ":" + branch}, "per_page": {"2"}}
	var listed []listedPull
	if err := api.do(ctx, apiCall{path: base + "/pulls?" + query.Encode(), want: http.StatusOK}, &listed); err != nil {
		return domain.PullRequest{}, fmt.Errorf("github: find pull request of %s on %s: %w", branch, repo, err)
	}
	return onePull(listed, repo, branch)
}

// onePull is the single listed pull request of branch.
func onePull(listed []listedPull, repo, branch string) (domain.PullRequest, error) {
	switch {
	case len(listed) == 0:
		return domain.PullRequest{}, fmt.Errorf("github: %s on %s: %w", branch, repo, ErrNoPullRequest)
	case len(listed) > 1:
		return domain.PullRequest{}, fmt.Errorf("github: %s on %s: %w", branch, repo, ErrManyPullRequests)
	case listed[0].Head.Ref != branch:
		return domain.PullRequest{}, fmt.Errorf("github: %s on %s: the listing answered a pull request of %q", branch, repo, listed[0].Head.Ref)
	}
	pr := listed[0]
	found := domain.PullRequest{Number: pr.Number, URL: pr.HTMLURL, Author: pr.User.Login, Base: pr.Base.Ref}
	if pr.Head.Repo != nil {
		found.HeadRepository = pr.Head.Repo.FullName
	}
	return found, nil
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
