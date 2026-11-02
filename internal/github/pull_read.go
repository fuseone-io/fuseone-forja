package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// Sentinels for a pull request asked of PullRequests.Read.
var (
	ErrPullRequestNotFound = errors.New("pull request not found")
	// ErrFilesTruncated means the files listed are not as many as the pull
	// request says it changes, as when it changes more than the API lists.
	ErrFilesTruncated = errors.New("changed files not all listed")
)

const (
	// filesPerPage is the most the files endpoint returns per page.
	filesPerPage = 100
	// maxFilePages is one page past the API's 3000-file cap, which answers
	// an empty page; a server that never runs short stops there.
	maxFilePages = 3000/filesPerPage + 1
	// maxFilesPage bounds one page of files, whose patches make it far
	// larger than any other response the control plane reads.
	maxFilesPage = 64 << 20
)

// pullDiffResponse is what Read decodes of a pull request. A body of null,
// which the API answers for a pull request with no description, decodes
// to "".
type pullDiffResponse struct {
	Number       int    `json:"number"`
	HTMLURL      string `json:"html_url"`
	Body         string `json:"body"`
	ChangedFiles int    `json:"changed_files"`
	Head         struct {
		Ref  string `json:"ref"`
		SHA  string `json:"sha"`
		Repo *struct {
			FullName string `json:"full_name"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

type fileResponse struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
}

// Read returns where the pull request's head is, the branch it targets and
// every file it changes, a rename with both its paths, with a token minted
// to read pull requests. An unknown number wraps ErrPullRequestNotFound; a
// pull request whose files are not all listed wraps ErrFilesTruncated.
// Errors name the repository and the number, never the pull request's
// title, body or paths.
func (p *PullRequests) Read(ctx context.Context, repo string, number int) (domain.PullRequestDiff, error) {
	base, err := repoPath(repo)
	if err != nil {
		return domain.PullRequestDiff{}, err
	}
	if number <= 0 {
		return domain.PullRequestDiff{}, fmt.Errorf("github: pull request number %d is not positive", number)
	}
	api, err := mintClient(ctx, scopedClient{tokens: p.Tokens, repo: repo, perms: ReadPullRequests, http: p.HTTP, base: p.BaseURL})
	if err != nil {
		return domain.PullRequestDiff{}, err
	}
	path := base + "/pulls/" + strconv.Itoa(number)
	var pr pullDiffResponse
	err = api.do(ctx, apiCall{path: path, want: http.StatusOK}, &pr)
	if isNotFound(err) {
		return domain.PullRequestDiff{}, fmt.Errorf("github: %s#%d: %w", repo, number, ErrPullRequestNotFound)
	}
	if err != nil {
		return domain.PullRequestDiff{}, fmt.Errorf("github: read pull request %s#%d: %w", repo, number, err)
	}
	files, err := listFiles(ctx, api, path)
	if err != nil {
		return domain.PullRequestDiff{}, fmt.Errorf("github: read files of %s#%d: %w", repo, number, err)
	}
	if len(files) != pr.ChangedFiles {
		return domain.PullRequestDiff{}, fmt.Errorf("github: %s#%d: %w: %d listed of %d changed", repo, number, ErrFilesTruncated, len(files), pr.ChangedFiles)
	}
	return diffOf(pr, files), nil
}

// listFiles reads the files a page at a time until a page runs short.
func listFiles(ctx context.Context, api *apiClient, pull string) ([]domain.ChangedFile, error) {
	var files []domain.ChangedFile
	for page := 1; page <= maxFilePages; page++ {
		var batch []fileResponse
		call := apiCall{path: pull + "/files?per_page=" + strconv.Itoa(filesPerPage) + "&page=" + strconv.Itoa(page), want: http.StatusOK, limit: maxFilesPage}
		if err := api.do(ctx, call, &batch); err != nil {
			return nil, err
		}
		for _, f := range batch {
			files = append(files, domain.ChangedFile{Path: f.Filename, PreviousPath: f.PreviousFilename})
		}
		if len(batch) < filesPerPage {
			break
		}
	}
	return files, nil
}

func diffOf(pr pullDiffResponse, files []domain.ChangedFile) domain.PullRequestDiff {
	diff := domain.PullRequestDiff{
		Number: pr.Number, URL: pr.HTMLURL, HeadRef: pr.Head.Ref, HeadSHA: pr.Head.SHA, BaseRef: pr.Base.Ref, Body: pr.Body, Files: files,
	}
	if pr.Head.Repo != nil {
		diff.HeadRepository = pr.Head.Repo.FullName
	}
	return diff
}
