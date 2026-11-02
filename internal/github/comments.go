package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
)

// CommentRequest is one comment with Body on pull request Number of
// Repository.
type CommentRequest struct {
	Repository string
	Number     int
	Body       string
}

// Comment posts the comment through the issues endpoint, which a token
// minted to write pull requests may use on a pull request. A repository
// that is not owner/name, a number that is not positive or an empty body is
// refused before a token is minted; a refusal by the API is a *StatusError
// that carries neither the body nor the token.
func (p *PullRequests) Comment(ctx context.Context, req CommentRequest) error {
	base, err := repoPath(req.Repository)
	if err != nil {
		return err
	}
	if req.Number <= 0 || req.Body == "" {
		return errors.New("github: a comment needs a pull request number and a body")
	}
	api, err := mintClient(ctx, scopedClient{tokens: p.Tokens, repo: req.Repository, perms: WritePullRequests, http: p.HTTP, base: p.BaseURL})
	if err != nil {
		return err
	}
	body, err := json.Marshal(struct {
		Body string `json:"body"`
	}{Body: req.Body})
	if err != nil {
		return fmt.Errorf("github: comment on %s#%d: %w", req.Repository, req.Number, err)
	}
	call := apiCall{path: base + "/issues/" + strconv.Itoa(req.Number) + "/comments", body: body, want: http.StatusCreated}
	if err := api.do(ctx, call, nil); err != nil {
		return fmt.Errorf("github: comment on %s#%d: %w", req.Repository, req.Number, err)
	}
	return nil
}
