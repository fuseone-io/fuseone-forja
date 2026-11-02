package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// The states a commit status takes.
const (
	StateError   = "error"
	StateFailure = "failure"
	StatePending = "pending"
	StateSuccess = "success"
)

// CommitStatuses publishes commit statuses through the API with a token
// minted to write statuses for each call.
type CommitStatuses struct {
	Tokens TokenSource
	// HTTP is the client used against BaseURL; nil means a client with a
	// default timeout.
	HTTP *http.Client
	// BaseURL is the API root; empty means DefaultBaseURL.
	BaseURL string
}

// StatusRequest is one status on commit SHA of Repository under Context.
type StatusRequest struct {
	Repository  string
	SHA         string
	State       string
	Context     string
	Description string
	TargetURL   string
}

// Publish posts the status. A full commit name, a known state and a
// context are checked before anything is minted; a refusal by the API,
// such as an unknown commit, is a *StatusError.
func (s *CommitStatuses) Publish(ctx context.Context, req StatusRequest) error {
	base, err := repoPath(req.Repository)
	if err != nil {
		return err
	}
	if err := req.validate(); err != nil {
		return fmt.Errorf("github: status on %s: %w", req.Repository, err)
	}
	api, err := mintClient(ctx, scopedClient{tokens: s.Tokens, repo: req.Repository, perms: WriteStatuses, http: s.HTTP, base: s.BaseURL})
	if err != nil {
		return err
	}
	body, err := json.Marshal(struct {
		State       string `json:"state"`
		Context     string `json:"context"`
		Description string `json:"description"`
		TargetURL   string `json:"target_url"`
	}{State: req.State, Context: req.Context, Description: req.Description, TargetURL: req.TargetURL})
	if err != nil {
		return fmt.Errorf("github: status on %s: %w", req.Repository, err)
	}
	call := apiCall{path: base + "/statuses/" + req.SHA, body: body, want: http.StatusCreated}
	if err := api.do(ctx, call, nil); err != nil {
		return fmt.Errorf("github: status %s on %s of %s: %w", req.Context, req.SHA, req.Repository, err)
	}
	return nil
}

func (r StatusRequest) validate() error {
	switch {
	case !commitSHA.MatchString(r.SHA):
		return fmt.Errorf("%q is not a commit name", r.SHA)
	case r.Context == "":
		return errors.New("a status needs a context")
	}
	switch r.State {
	case StateError, StateFailure, StatePending, StateSuccess:
		return nil
	}
	return fmt.Errorf("state %q is not a commit status state", r.State)
}
