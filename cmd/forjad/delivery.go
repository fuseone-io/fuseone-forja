package main

import (
	"context"
	"fmt"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/github"
	"github.com/fuseone-io/fuseone-forja/internal/mcp"
)

// withDelivery gives forja.implement what delivers a green run: the push,
// the pull request or the comment on it, and the status, each minting its
// own token from the control plane's source, and the login that source
// acts as; and forja.verify what reads the pull request it verifies.
// Nothing of it reaches a workspace. A source that cannot name its login
// is a startup failure.
func withDelivery(deps *mcp.Deps, tokens github.TokenSource) error {
	id, err := identityOf(tokens)
	if err != nil {
		return err
	}
	deps.Identity = id
	deps.Pusher = &github.Pusher{Tokens: tokens}
	deps.PullRequests = &github.PullRequests{Tokens: tokens}
	deps.Statuses = &github.CommitStatuses{Tokens: tokens}
	deps.PullRequestReader = &github.PullRequests{Tokens: tokens}
	return nil
}

// identityOf is the token source as the login a correction round compares
// a pull request's author with.
func identityOf(tokens github.TokenSource) (mcp.Identity, error) {
	id, ok := tokens.(mcp.Identity)
	if !ok {
		return nil, fmt.Errorf("github credential %T cannot name the login it acts as", tokens)
	}
	return id, nil
}

// withoutDelivery is withDelivery when no credential is set: every call
// answers with err.
func withoutDelivery(deps *mcp.Deps, err error) {
	u := unavailableDelivery{err: err}
	deps.Pusher, deps.PullRequests, deps.Statuses, deps.Identity, deps.PullRequestReader = u, u, u, u, u
}

// unavailableDelivery is the pusher, pull requests, statuses, identity and
// pull request reader served when no credential is set.
type unavailableDelivery struct {
	err error
}

func (u unavailableDelivery) Login(context.Context) (string, error) { return "", u.err }

func (u unavailableDelivery) Push(context.Context, github.PushRequest) error { return u.err }

func (u unavailableDelivery) Open(context.Context, github.PullRequestRequest) (domain.PullRequest, error) {
	return domain.PullRequest{}, u.err
}

func (u unavailableDelivery) Find(context.Context, string, string) (domain.PullRequest, error) {
	return domain.PullRequest{}, u.err
}

func (u unavailableDelivery) Comment(context.Context, github.CommentRequest) error { return u.err }

func (u unavailableDelivery) Publish(context.Context, github.StatusRequest) error { return u.err }

func (u unavailableDelivery) Read(context.Context, string, int) (domain.PullRequestDiff, error) {
	return domain.PullRequestDiff{}, u.err
}
