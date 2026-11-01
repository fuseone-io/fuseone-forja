package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// Sentinels for a number asked of an IssueReader.
var (
	ErrIssueNotFound = errors.New("issue not found")
	ErrNotAnIssue    = errors.New("number is a pull request, not an issue")
)

// IssueReader fetches one issue through the API with a token minted for the
// repository. Errors name the repository and the number, never the issue's
// title or body.
type IssueReader struct {
	Tokens TokenSource
	// HTTP is the client used against BaseURL; nil means a client with a
	// default timeout.
	HTTP *http.Client
	// BaseURL is the API root; empty means DefaultBaseURL.
	BaseURL string
}

// issueResponse is the subset of the API's issue object the reader uses.
// Body is a pointer because the API answers null for an issue created
// without one; PullRequest is present only when the number is a PR.
type issueResponse struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        *string   `json:"body"`
	PullRequest *struct{} `json:"pull_request"`
}

// Read returns the issue's statement. An unknown number wraps
// ErrIssueNotFound; a pull request number wraps ErrNotAnIssue.
func (r *IssueReader) Read(ctx context.Context, repo string, number int) (domain.Issue, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return domain.Issue{}, err
	}
	if number <= 0 {
		return domain.Issue{}, fmt.Errorf("github: issue number %d is not positive", number)
	}
	if r.Tokens == nil {
		return domain.Issue{}, errors.New("github: IssueReader has no token source")
	}
	tok, err := r.Tokens.Token(ctx, repo)
	if err != nil {
		return domain.Issue{}, fmt.Errorf("github: read issue %s#%d: %w", repo, number, err)
	}
	api := newAPIClient(r.HTTP, r.BaseURL, tok.Value)
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/issues/" + strconv.Itoa(number)
	var res issueResponse
	err = api.do(ctx, apiCall{path: path, want: http.StatusOK}, &res)
	if isNotFound(err) {
		return domain.Issue{}, fmt.Errorf("github: %s#%d: %w", repo, number, ErrIssueNotFound)
	}
	if err != nil {
		return domain.Issue{}, fmt.Errorf("github: read issue %s#%d: %w", repo, number, err)
	}
	if res.PullRequest != nil {
		return domain.Issue{}, fmt.Errorf("github: %s#%d: %w", repo, number, ErrNotAnIssue)
	}
	issue := domain.Issue{Number: res.Number, Title: res.Title}
	if res.Body != nil {
		issue.Body = *res.Body
	}
	return issue, nil
}
