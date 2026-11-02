package mcp_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/github"
)

// deliveryLog is what a delivery fake records: each request it got and
// the deadline it ran under.
type deliveryLog[R any] struct {
	calls *callLog
	name  string
	err   error

	mu        sync.Mutex
	requests  []R
	deadlines []time.Time
}

func (d *deliveryLog[R]) record(ctx context.Context, req R) error {
	if d.calls != nil {
		d.calls.add(d.name)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.requests = append(d.requests, req)
	dl, _ := ctx.Deadline()
	d.deadlines = append(d.deadlines, dl)
	return d.err
}

func (d *deliveryLog[R]) got() []R {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]R(nil), d.requests...)
}

func (d *deliveryLog[R]) deadline() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.deadlines) == 0 {
		return time.Time{}
	}
	return d.deadlines[0]
}

// fakePusher accepts every push, or fails with err; like the real one it
// refuses a destination that is not forja/<issue>.
type fakePusher struct {
	deliveryLog[github.PushRequest]
}

func (f *fakePusher) Push(ctx context.Context, req github.PushRequest) error {
	if err := f.record(ctx, req); err != nil {
		return err
	}
	if req.Branch != "forja/12" || req.Head == "" {
		return errPushRefused
	}
	return nil
}

// fakePullRequests opens one pull request, numbered as the API numbered
// the pilot's, or fails with err. Find answers found for the run's branch,
// ErrNoPullRequest when found is zero, or fails with finds.err; Comment
// records the comment or fails with comments.err. Like the real one, a
// comment needs a number and a body.
type fakePullRequests struct {
	deliveryLog[github.PullRequestRequest]
	finds    deliveryLog[string]
	comments deliveryLog[github.CommentRequest]
	found    domain.PullRequest
}

const prURL = "https://github.com/" + doctorRepo + "/pull/41"

func (f *fakePullRequests) Open(ctx context.Context, req github.PullRequestRequest) (domain.PullRequest, error) {
	if err := f.record(ctx, req); err != nil {
		return domain.PullRequest{}, err
	}
	return domain.PullRequest{Number: 41, URL: prURL}, nil
}

func (f *fakePullRequests) Find(ctx context.Context, repo, branch string) (domain.PullRequest, error) {
	if err := f.finds.record(ctx, repo+" "+branch); err != nil {
		return domain.PullRequest{}, err
	}
	if f.found.Number == 0 {
		return domain.PullRequest{}, fmt.Errorf("fake: %s: %w", branch, github.ErrNoPullRequest)
	}
	return f.found, nil
}

func (f *fakePullRequests) Comment(ctx context.Context, req github.CommentRequest) error {
	if err := f.comments.record(ctx, req); err != nil {
		return err
	}
	if req.Number <= 0 || req.Body == "" {
		return errors.New("fake: a comment needs a pull request number and a body")
	}
	return nil
}

// fakeStatuses accepts every status, or fails with err.
type fakeStatuses struct {
	deliveryLog[github.StatusRequest]
}

func (f *fakeStatuses) Publish(ctx context.Context, req github.StatusRequest) error {
	return f.record(ctx, req)
}

type refusal string

func (r refusal) Error() string { return string(r) }

const errPushRefused = refusal("fake: push to a branch that is not the run's")
