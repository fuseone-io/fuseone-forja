package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// FileCommit is one file to store as a commit of its own: a tree holding
// only Path, with Parent as the single parent or none when Parent is empty.
type FileCommit struct {
	Path    string
	Body    []byte
	Message string
	Parent  string
}

type objectResponse struct {
	SHA string `json:"sha"`
}

// treeRequestEntry is one entry of POST /git/trees.
type treeRequestEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

// CommitFile writes the blob, a tree with that one entry and the commit,
// and returns the commit's sha. Nothing points at it until a ref does.
func (r *Refs) CommitFile(ctx context.Context, repo string, file FileCommit) (string, error) {
	if file.Path == "" || strings.Contains(file.Path, "/") {
		return "", fmt.Errorf("github: commit file of %s: path %q must be a single file name", repo, file.Path)
	}
	api, base, err := r.client(ctx, repo)
	if err != nil {
		return "", err
	}
	blob, err := post(ctx, api, base+"/git/blobs", map[string]string{
		"content": base64.StdEncoding.EncodeToString(file.Body), "encoding": "base64",
	})
	if err != nil {
		return "", fmt.Errorf("github: commit file of %s: blob: %w", repo, err)
	}
	tree, err := post(ctx, api, base+"/git/trees", map[string][]treeRequestEntry{
		"tree": {{Path: file.Path, Mode: "100644", Type: "blob", SHA: blob}},
	})
	if err != nil {
		return "", fmt.Errorf("github: commit file of %s: tree: %w", repo, err)
	}
	parents := []string{}
	if file.Parent != "" {
		parents = []string{file.Parent}
	}
	commit, err := post(ctx, api, base+"/git/commits", struct {
		Message string   `json:"message"`
		Tree    string   `json:"tree"`
		Parents []string `json:"parents"`
	}{Message: file.Message, Tree: tree, Parents: parents})
	if err != nil {
		return "", fmt.Errorf("github: commit file of %s: commit: %w", repo, err)
	}
	return commit, nil
}

// post creates one Git object and returns its sha. payload is any because
// it is handed to json.Marshal, which takes any.
func post(ctx context.Context, api *apiClient, path string, payload any) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	var created objectResponse
	if err := api.do(ctx, apiCall{path: path, body: body, want: http.StatusCreated}, &created); err != nil {
		return "", err
	}
	if created.SHA == "" {
		return "", fmt.Errorf("POST %s: response carries no sha", path)
	}
	return created.SHA, nil
}

// ReadFile returns the bytes of path at the commit sha, which need not be
// on any branch.
func (r *Refs) ReadFile(ctx context.Context, repo, sha, path string) ([]byte, error) {
	api, base, err := r.client(ctx, repo)
	if err != nil {
		return nil, err
	}
	var file contentsResponse
	call := apiCall{path: base + "/contents/" + url.PathEscape(path) + "?ref=" + url.QueryEscape(sha), want: http.StatusOK}
	if err := api.do(ctx, call, &file); err != nil {
		return nil, fmt.Errorf("github: read %s of %s at %s: %w", path, repo, sha, err)
	}
	body, err := decodeContents(file)
	if err != nil {
		return nil, fmt.Errorf("github: read %s of %s at %s: %w", path, repo, sha, err)
	}
	return body, nil
}
