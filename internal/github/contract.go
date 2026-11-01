package github

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// ErrContractMissing means the default branch's commit has no .forja.yaml.
var ErrContractMissing = errors.New("contract missing")

// ContractReader fetches .forja.yaml from the commit at the tip of the
// repository's default branch, through the API, with a token minted for
// that repository. The SHA it returns is that commit's, never the blob's.
type ContractReader struct {
	Tokens TokenSource
	// HTTP is the client used against BaseURL; nil means a client with a
	// default timeout.
	HTTP *http.Client
	// BaseURL is the API root; empty means DefaultBaseURL.
	BaseURL string
}

type repositoryResponse struct {
	DefaultBranch string `json:"default_branch"`
}

type branchResponse struct {
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

type contentsResponse struct {
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

// Read resolves the default branch, pins its commit and reads the contract
// at that commit. A missing file wraps ErrContractMissing and names the SHA.
func (r *ContractReader) Read(ctx context.Context, repo string) (domain.ContractSource, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return domain.ContractSource{}, err
	}
	if r.Tokens == nil {
		return domain.ContractSource{}, errors.New("github: ContractReader has no token source")
	}
	tok, err := r.Tokens.Token(ctx, repo)
	if err != nil {
		return domain.ContractSource{}, fmt.Errorf("github: read contract of %s: %w", repo, err)
	}
	api := newAPIClient(r.HTTP, r.BaseURL, tok.Value)
	base := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)

	sha, err := defaultBranchCommit(ctx, api, base)
	if err != nil {
		return domain.ContractSource{}, fmt.Errorf("github: read contract of %s: %w", repo, err)
	}
	var file contentsResponse
	err = api.do(ctx, apiCall{path: base + "/contents/.forja.yaml?ref=" + url.QueryEscape(sha), want: http.StatusOK}, &file)
	if isNotFound(err) {
		return domain.ContractSource{}, fmt.Errorf("github: %s: %w at %s", repo, ErrContractMissing, sha)
	}
	if err != nil {
		return domain.ContractSource{}, fmt.Errorf("github: read contract of %s at %s: %w", repo, sha, err)
	}
	body, err := decodeContents(file)
	if err != nil {
		return domain.ContractSource{}, fmt.Errorf("github: read contract of %s at %s: %w", repo, sha, err)
	}
	return domain.ContractSource{SHA: sha, Body: body}, nil
}

// defaultBranchCommit returns the SHA at the tip of the default branch.
func defaultBranchCommit(ctx context.Context, api *apiClient, base string) (string, error) {
	var repository repositoryResponse
	if err := api.do(ctx, apiCall{path: base, want: http.StatusOK}, &repository); err != nil {
		return "", err
	}
	if repository.DefaultBranch == "" {
		return "", errors.New("repository has no default branch")
	}
	var branch branchResponse
	path := base + "/branches/" + url.PathEscape(repository.DefaultBranch)
	if err := api.do(ctx, apiCall{path: path, want: http.StatusOK}, &branch); err != nil {
		return "", err
	}
	if branch.Commit.SHA == "" {
		return "", fmt.Errorf("branch %s has no commit", repository.DefaultBranch)
	}
	return branch.Commit.SHA, nil
}

// decodeContents decodes the file body; the API chunks the base64 with
// newlines, which the decoder ignores.
func decodeContents(file contentsResponse) ([]byte, error) {
	if file.Encoding != "base64" {
		return nil, fmt.Errorf("contents encoding %q is not base64", file.Encoding)
	}
	body, err := base64.StdEncoding.DecodeString(file.Content)
	if err != nil {
		return nil, fmt.Errorf("decode contents: %w", err)
	}
	return body, nil
}
