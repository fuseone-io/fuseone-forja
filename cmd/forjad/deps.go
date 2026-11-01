package main

import (
	"context"
	"errors"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/gates"
	"github.com/fuseone-io/fuseone-forja/internal/github"
	"github.com/fuseone-io/fuseone-forja/internal/mcp"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
	"github.com/fuseone-io/fuseone-forja/internal/workspace"
)

// buildDeps assembles the server's dependencies from the environment. With
// no GitHub credentials at all the server still serves the tools that need
// none, and every GitHub-backed tool answers with the error naming the
// variables to set. A half-configured App, or a Docker client that cannot
// be built from DOCKER_HOST and its siblings, is a startup failure.
func buildDeps(getenv func(string) string) (mcp.Deps, error) {
	runs := runstore.NewMemory()
	docker, err := workspace.NewDocker()
	if err != nil {
		return mcp.Deps{}, err
	}
	runner := &gates.Runner{Launcher: launcher{docker}, Now: time.Now}
	tokens, err := github.TokenSourceFromEnv(getenv)
	if errors.Is(err, github.ErrNoCredentials) {
		return mcp.Deps{Runs: runs, Cloner: unavailablePlan{err}, Issues: unavailablePlan{err}, Gates: runner}, nil
	}
	if err != nil {
		return mcp.Deps{}, err
	}
	return mcp.Deps{
		Runs:   runs,
		Cloner: cloner{&github.Cloner{Tokens: tokens}},
		Issues: &github.IssueReader{Tokens: tokens},
		Gates:  runner,
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

// launcher adapts workspace.Docker, which returns its own types, to the
// interfaces the gates runner declares.
type launcher struct {
	*workspace.Docker
}

func (l launcher) Launch(ctx context.Context, image, tree string) (gates.Workspace, error) {
	c, err := l.Docker.Launch(ctx, image, tree)
	if err != nil {
		return nil, err
	}
	return container{c}, nil
}

type container struct {
	*workspace.Container
}

func (c container) Exec(ctx context.Context, command string) (gates.ExecResult, error) {
	res, err := c.Container.Exec(ctx, command)
	return gates.ExecResult{ExitCode: res.ExitCode, Stdout: res.Stdout, Stderr: res.Stderr}, err
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
