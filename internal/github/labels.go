package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
)

// LabelRequest adds Labels to pull request Number of Repository.
type LabelRequest struct {
	Repository string
	Number     int
	Labels     []string
}

// Label adds the labels to the pull request through the issues API, which
// labels a pull request by its number, with a token minted to write pull
// requests. A malformed repository, a number that is not positive or a
// list that is empty or holds an empty name is refused before anything is
// minted; a refusal by the API, such as an unknown number, is a
// *StatusError.
func (p *PullRequests) Label(ctx context.Context, req LabelRequest) error {
	base, err := repoPath(req.Repository)
	if err != nil {
		return err
	}
	if req.Number <= 0 || len(req.Labels) == 0 || slices.Contains(req.Labels, "") {
		return errors.New("github: a label needs a pull request number and a non-empty name")
	}
	api, err := mintClient(ctx, scopedClient{tokens: p.Tokens, repo: req.Repository, perms: WritePullRequests, http: p.HTTP, base: p.BaseURL})
	if err != nil {
		return err
	}
	body, err := json.Marshal(struct {
		Labels []string `json:"labels"`
	}{Labels: req.Labels})
	if err != nil {
		return fmt.Errorf("github: label pull request %d on %s: %w", req.Number, req.Repository, err)
	}
	call := apiCall{path: base + "/issues/" + strconv.Itoa(req.Number) + "/labels", body: body, want: http.StatusOK}
	if err := api.do(ctx, call, nil); err != nil {
		return fmt.Errorf("github: label pull request %d on %s: %w", req.Number, req.Repository, err)
	}
	return nil
}
