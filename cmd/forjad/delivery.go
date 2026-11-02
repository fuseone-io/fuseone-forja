package main

import (
	"context"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/github"
	"github.com/fuseone-io/fuseone-forja/internal/mcp"
)

// withDelivery gives forja.implement what delivers a green run, the push,
// the pull request and the status, and forja.verify what reads the pull
// request it verifies, each minting its own token from the control
// plane's source. Nothing of it reaches a workspace.
func withDelivery(deps *mcp.Deps, tokens github.TokenSource) {
	deps.Pusher = &github.Pusher{Tokens: tokens}
	deps.PullRequests = &github.PullRequests{Tokens: tokens}
	deps.Statuses = &github.CommitStatuses{Tokens: tokens}
	deps.PullRequestReader = &github.PullRequests{Tokens: tokens}
}

// withoutDelivery is withDelivery when no credential is set: every call
// answers with err.
func withoutDelivery(deps *mcp.Deps, err error) {
	u := unavailableDelivery{err: err}
	deps.Pusher, deps.PullRequests, deps.Statuses, deps.PullRequestReader = u, u, u, u
}

// unavailableDelivery is the pusher, pull requests, statuses and pull
// request reader served when no credential is set.
type unavailableDelivery struct {
	err error
}

func (u unavailableDelivery) Push(context.Context, github.PushRequest) error { return u.err }

func (u unavailableDelivery) Open(context.Context, github.PullRequestRequest) (domain.PullRequest, error) {
	return domain.PullRequest{}, u.err
}

func (u unavailableDelivery) Publish(context.Context, github.StatusRequest) error { return u.err }

func (u unavailableDelivery) Read(context.Context, string, int) (domain.PullRequestDiff, error) {
	return domain.PullRequestDiff{}, u.err
}
