package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// The outcomes of a ref call that the caller decides on. Each is read from
// the status the API answers with, never from its message.
var (
	// ErrRefExists means a create found the ref already there.
	ErrRefExists = errors.New("ref exists")
	// ErrRefMissing means the ref does not exist.
	ErrRefMissing = errors.New("ref missing")
	// ErrNotFastForward means an update would move the ref to a commit that
	// does not descend from its current one.
	ErrNotFastForward = errors.New("ref update is not a fast-forward")
)

// RefObject is what a ref points at.
type RefObject struct {
	SHA  string
	Type string
}

// Refs creates, moves and deletes refs of a repository through the API,
// with a token minted to write contents for each call. An update is never
// forced: moving a ref to a commit that does not descend from its current
// value is refused by the API and reported as ErrNotFastForward.
type Refs struct {
	Tokens TokenSource
	// HTTP is the client used against BaseURL; nil means a client with a
	// default timeout.
	HTTP *http.Client
	// BaseURL is the API root; empty means DefaultBaseURL.
	BaseURL string
}

type refResponse struct {
	Ref    string `json:"ref"`
	Object struct {
		SHA  string `json:"sha"`
		Type string `json:"type"`
	} `json:"object"`
}

// GetRef returns the object a ref points at, or wraps ErrRefMissing.
func (r *Refs) GetRef(ctx context.Context, repo, ref string) (RefObject, error) {
	api, base, err := r.refClient(ctx, repo, ref)
	if err != nil {
		return RefObject{}, err
	}
	var res refResponse
	err = api.do(ctx, apiCall{path: base + "/git/ref/" + refPath(ref), want: http.StatusOK}, &res)
	if isNotFound(err) {
		return RefObject{}, fmt.Errorf("github: %s: %s: %w", repo, ref, ErrRefMissing)
	}
	if err != nil {
		return RefObject{}, fmt.Errorf("github: get ref %s of %s: %w", ref, repo, err)
	}
	return RefObject{SHA: res.Object.SHA, Type: res.Object.Type}, nil
}

// CreateRef points a new ref at sha. A ref already there wraps ErrRefExists.
func (r *Refs) CreateRef(ctx context.Context, repo, ref, sha string) error {
	api, base, err := r.refClient(ctx, repo, ref)
	if err != nil {
		return err
	}
	body, err := json.Marshal(struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	}{Ref: ref, SHA: sha})
	if err != nil {
		return fmt.Errorf("github: create ref %s of %s: %w", ref, repo, err)
	}
	err = api.do(ctx, apiCall{path: base + "/git/refs", body: body, want: http.StatusCreated}, &refResponse{})
	if isStatus(err, http.StatusUnprocessableEntity) {
		return fmt.Errorf("github: %s: %s: %w: %w", repo, ref, ErrRefExists, err)
	}
	if err != nil {
		return fmt.Errorf("github: create ref %s of %s: %w", ref, repo, err)
	}
	return nil
}

// UpdateRef moves an existing ref to sha, only forward: the API refuses a
// sha that does not descend from the current one, reported as
// ErrNotFastForward.
func (r *Refs) UpdateRef(ctx context.Context, repo, ref, sha string) error {
	api, base, err := r.refClient(ctx, repo, ref)
	if err != nil {
		return err
	}
	body, err := json.Marshal(struct {
		SHA   string `json:"sha"`
		Force bool   `json:"force"`
	}{SHA: sha, Force: false})
	if err != nil {
		return fmt.Errorf("github: update ref %s of %s: %w", ref, repo, err)
	}
	call := apiCall{method: http.MethodPatch, path: base + "/git/refs/" + refPath(ref), body: body, want: http.StatusOK}
	err = api.do(ctx, call, &refResponse{})
	if isStatus(err, http.StatusUnprocessableEntity) {
		return fmt.Errorf("github: %s: %s: %w: %w", repo, ref, r.updateRefusal(ctx, api, base, ref), err)
	}
	if err != nil {
		return fmt.Errorf("github: update ref %s of %s: %w", ref, repo, err)
	}
	return nil
}

// updateRefusal tells a refused update apart: the API answers the same
// status for a ref that is not there and for a move that is not a
// fast-forward, so the ref is looked up once more.
func (r *Refs) updateRefusal(ctx context.Context, api *apiClient, base, ref string) error {
	err := api.do(ctx, apiCall{path: base + "/git/ref/" + refPath(ref), want: http.StatusOK}, &refResponse{})
	if isNotFound(err) {
		return ErrRefMissing
	}
	return ErrNotFastForward
}

// DeleteRef removes a ref. A ref that is not there wraps ErrRefMissing.
func (r *Refs) DeleteRef(ctx context.Context, repo, ref string) error {
	api, base, err := r.refClient(ctx, repo, ref)
	if err != nil {
		return err
	}
	call := apiCall{method: http.MethodDelete, path: base + "/git/refs/" + refPath(ref), want: http.StatusNoContent}
	err = api.do(ctx, call, nil)
	if isStatus(err, http.StatusUnprocessableEntity) {
		return fmt.Errorf("github: %s: %s: %w: %w", repo, ref, ErrRefMissing, err)
	}
	if err != nil {
		return fmt.Errorf("github: delete ref %s of %s: %w", ref, repo, err)
	}
	return nil
}

// refClient validates the ref before anything is minted.
func (r *Refs) refClient(ctx context.Context, repo, ref string) (*apiClient, string, error) {
	if err := validRef(ref); err != nil {
		return nil, "", fmt.Errorf("github: %s: %w", repo, err)
	}
	return r.client(ctx, repo)
}

// client validates the repository and mints a token that can write its
// contents; the base path of the repository comes with it.
func (r *Refs) client(ctx context.Context, repo string) (*apiClient, string, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, "", err
	}
	if r.Tokens == nil {
		return nil, "", errors.New("github: Refs has no token source")
	}
	tok, err := r.Tokens.Token(ctx, repo, WriteContents)
	if err != nil {
		return nil, "", fmt.Errorf("github: %s: %w", repo, err)
	}
	base := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
	return newAPIClient(r.HTTP, r.BaseURL, tok.Value), base, nil
}

// validRef accepts only a fully qualified ref with no empty or dot segment.
func validRef(ref string) error {
	rest, ok := strings.CutPrefix(ref, "refs/")
	if !ok || rest == "" {
		return fmt.Errorf("ref %q is not fully qualified", ref)
	}
	for _, segment := range strings.Split(rest, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("ref %q has an empty or dot segment", ref)
		}
	}
	return nil
}

// refPath is the ref as the API addresses it: without "refs/", each
// segment escaped.
func refPath(ref string) string {
	segments := strings.Split(strings.TrimPrefix(ref, "refs/"), "/")
	for i, s := range segments {
		segments[i] = url.PathEscape(s)
	}
	return strings.Join(segments, "/")
}

func isStatus(err error, status int) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Status == status
}
