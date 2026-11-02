package mcp_test

import (
	"context"
	"fmt"
	"sync"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/github"
)

// fakePullReader serves the pull requests it holds as the real reader
// does: a number that is not positive is refused before anything is
// asked, and a number or repository it does not hold wraps
// github.ErrPullRequestNotFound. It hands out a copy, so a handler that
// changed the files could not change what a later read sees.
type fakePullReader struct {
	calls *callLog
	repo  string
	prs   map[int]domain.PullRequestDiff
	err   error

	mu    sync.Mutex
	asked []int
}

func (f *fakePullReader) Read(_ context.Context, repo string, number int) (domain.PullRequestDiff, error) {
	if f.calls != nil {
		f.calls.add("read pull request")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, number)
	if number <= 0 {
		return domain.PullRequestDiff{}, fmt.Errorf("fake: pull request number %d is not positive", number)
	}
	if f.err != nil {
		return domain.PullRequestDiff{}, f.err
	}
	pr, ok := f.prs[number]
	if !ok || repo != f.repo {
		return domain.PullRequestDiff{}, fmt.Errorf("fake: %s#%d: %w", repo, number, github.ErrPullRequestNotFound)
	}
	pr.Files = append([]domain.ChangedFile(nil), pr.Files...)
	return pr, nil
}
