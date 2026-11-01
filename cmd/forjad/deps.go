package main

import (
	"context"
	"errors"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/github"
	"github.com/fuseone-io/fuseone-forja/internal/mcp"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

// buildDeps assembles the server's dependencies from the environment. With
// no credentials at all the server still serves the tools that need none,
// and every GitHub-backed tool answers with the error naming the variables
// to set. A half-configured App is a startup failure.
func buildDeps(getenv func(string) string) (mcp.Deps, error) {
	runs := runstore.NewMemory()
	tokens, err := github.TokenSourceFromEnv(getenv)
	if errors.Is(err, github.ErrNoCredentials) {
		return mcp.Deps{Runs: runs, Contracts: unavailableContracts{err}, Cloner: unavailablePlan{err}, Issues: unavailablePlan{err}}, nil
	}
	if err != nil {
		return mcp.Deps{}, err
	}
	return mcp.Deps{
		Runs:      runs,
		Contracts: &github.ContractReader{Tokens: tokens},
		Cloner:    cloner{&github.Cloner{Tokens: tokens}},
		Issues:    &github.IssueReader{Tokens: tokens},
	}, nil
}

// cloner adapts github.Cloner, which returns its own struct, to the
// interface forja.plan declares. A failed clone yields a nil interface,
// never a typed nil inside one.
type cloner struct {
	*github.Cloner
}

func (c cloner) Clone(ctx context.Context, repo, branch string) (mcp.Checkout, error) {
	co, err := c.Cloner.Clone(ctx, repo, branch)
	if err != nil {
		return nil, err
	}
	return co, nil
}

// unavailableContracts is the contract reader served when no credential is
// set: every call fails with the configuration error.
type unavailableContracts struct {
	err error
}

func (u unavailableContracts) Read(context.Context, string) (domain.ContractSource, error) {
	return domain.ContractSource{}, u.err
}

// unavailablePlan is the cloner and issue reader served when no credential
// is set.
type unavailablePlan struct {
	err error
}

func (u unavailablePlan) Clone(context.Context, string, string) (mcp.Checkout, error) {
	return nil, u.err
}

func (u unavailablePlan) Read(context.Context, string, int) (domain.Issue, error) {
	return domain.Issue{}, u.err
}
