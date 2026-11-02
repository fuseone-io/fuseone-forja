package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/claim"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/gates"
	"github.com/fuseone-io/fuseone-forja/internal/github"
	"github.com/fuseone-io/fuseone-forja/internal/mcp"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
	"github.com/fuseone-io/fuseone-forja/internal/workspace"
)

// The environment variables the claims are configured from.
const (
	// EnvStateDir is the directory the claim journal lives under. Unset,
	// a claim left by a crash is not recovered on start.
	EnvStateDir = "FORJA_STATE_DIR"
	// EnvClaimLease overrides the lease a run holds its issue for, as a Go
	// duration. It must outlive the contract's wall clock.
	EnvClaimLease = "FORJA_CLAIM_LEASE"
)

// wire assembles the tools' dependencies from the environment. With no
// credentials at all the server still serves the tools that need none,
// and every GitHub-backed tool answers with the error naming the variables
// to set; the claim store is then nil. A half-configured App, a malformed
// lease, price table, egress allowlist, registry credentials file or model
// configuration, or a Docker client that cannot be built from DOCKER_HOST
// and its siblings is a startup failure; an unset workspace runtime is a
// warning. The wall clock and the
// id generator are injected here, at the edge.
func wire(getenv func(string) string, stderr io.Writer) (mcp.Deps, *claim.Store, error) {
	deps, err := baseDeps(getenv)
	if err != nil {
		return mcp.Deps{}, nil, err
	}
	opts, agentOn, err := driverOptions(getenv, stderr)
	if err != nil {
		return mcp.Deps{}, nil, err
	}
	docker, err := workspace.NewDocker(opts)
	if err != nil {
		return mcp.Deps{}, nil, err
	}
	deps.Gates = &gates.Runner{Launcher: launcher{docker}, Now: time.Now}
	deps.Agent = agentRunner(docker, agentOn)
	tokens, err := github.TokenSourceFromEnv(getenv)
	if errors.Is(err, github.ErrNoCredentials) {
		deps.Contracts = unavailableContracts{err: err}
		deps.Claims = unavailableClaims{err: err}
		deps.Cloner = unavailablePlan{err: err}
		deps.BranchCloner = unavailablePlan{err: err}
		deps.Issues = unavailablePlan{err: err}
		withoutDelivery(&deps, err)
		if deps.Agent == nil {
			deps.Agent = unavailableAgent{err: err}
		}
		return deps, nil, nil
	}
	if err != nil {
		return mcp.Deps{}, nil, err
	}
	if err := withGitHub(&deps, tokens); err != nil {
		return mcp.Deps{}, nil, err
	}
	store := claim.NewStore(&github.Refs{Tokens: tokens}, utcNow, journal(getenv, stderr))
	deps.Claims = store
	return deps, store, nil
}

// withGitHub gives the tools the control plane's GitHub clients, all
// minting from tokens: the contract, both clones, the issue and the
// delivery.
func withGitHub(deps *mcp.Deps, tokens github.TokenSource) error {
	deps.Contracts = &github.ContractReader{Tokens: tokens}
	c := cloner{&github.Cloner{Tokens: tokens}}
	deps.Cloner, deps.BranchCloner = c, c
	deps.Issues = &github.IssueReader{Tokens: tokens}
	return withDelivery(deps, tokens)
}

// baseDeps is what every server has, however it is configured: the run
// store, the clock and the ids, and the operator's lease and price table.
func baseDeps(getenv func(string) string) (mcp.Deps, error) {
	deps := mcp.Deps{Runs: runstore.NewMemory(), NewID: newRunID, Now: utcNow, Version: version}
	var err error
	if deps.Lease, err = claimLease(getenv); err != nil {
		return mcp.Deps{}, err
	}
	if deps.Prices, err = priceTable(getenv); err != nil {
		return mcp.Deps{}, err
	}
	return deps, nil
}

// driverOptions is the workspace driver's configuration: the workspaces'
// own, and the agent with its model proxy when a key is set.
func driverOptions(getenv func(string) string, stderr io.Writer) (workspace.Options, bool, error) {
	opts, err := workspaceOptions(getenv, stderr)
	if err != nil {
		return workspace.Options{}, false, err
	}
	agentOpts, agentOn, err := agentConfig(getenv)
	if err != nil {
		return workspace.Options{}, false, err
	}
	opts.AgentBinary, opts.Model = agentOpts.AgentBinary, agentOpts.Model
	return opts, agentOn, nil
}

func claimLease(getenv func(string) string) (time.Duration, error) {
	raw := getenv(EnvClaimLease)
	if raw == "" {
		return 0, nil
	}
	lease, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", EnvClaimLease, err)
	}
	if lease <= 0 {
		return 0, fmt.Errorf("%s: %v is not a positive duration", EnvClaimLease, lease)
	}
	return lease, nil
}

// journal is the claim journal under the state directory, or nil with one
// warning when no directory is configured.
func journal(getenv func(string) string, stderr io.Writer) *claim.Journal {
	dir := getenv(EnvStateDir)
	if dir == "" {
		// The exit code is the only channel left when stderr itself fails.
		_, _ = fmt.Fprintf(stderr, "forjad: %s is not set: a claim left by a crash is not recovered on start and is freed only when its lease expires\n", EnvStateDir)
		return nil
	}
	return &claim.Journal{Dir: filepath.Join(dir, "claims")}
}

// recoverClaims releases what a previous process left behind and reports
// each on stderr by repository, issue and run id.
func recoverClaims(ctx context.Context, store *claim.Store, stderr io.Writer) error {
	if store == nil {
		return nil
	}
	released, err := store.Recover(ctx)
	if err != nil {
		return fmt.Errorf("recover claims: %w", err)
	}
	for _, c := range released {
		// The exit code is the only channel left when stderr itself fails.
		_, _ = fmt.Fprintf(stderr, "forjad: released the claim of issue %d of %s left by run %s\n", c.Issue, c.Repository, c.RunID)
	}
	return nil
}

// utcNow is the wall clock every run and claim is stamped with: instants
// are stored in UTC, whatever the host's zone.
func utcNow() time.Time { return time.Now().UTC() }

// newRunID is 16 random bytes in hex. rand.Read never returns an error:
// it fills the buffer or stops the process.
func newRunID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
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

// CloneBranch is the correction round's clone, adapted the same way.
func (c cloner) CloneBranch(ctx context.Context, repo, base, branch string) (mcp.Checkout, error) {
	co, err := c.Cloner.CloneBranch(ctx, repo, base, branch)
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

func (l launcher) Launch(ctx context.Context, spec gates.LaunchSpec) (gates.Workspace, error) {
	// Field for field the same, like the exec result.
	c, err := l.Docker.Launch(ctx, workspace.LaunchSpec(spec))
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
	// The two result types are field for field the same; the conversion
	// makes the compiler refuse an adapter that drops one.
	return gates.ExecResult(res), err
}

func (c container) StartService(ctx context.Context, name string, svc domain.Service, readiness time.Duration) (gates.ServiceResult, error) {
	res, err := c.Container.StartService(ctx, name, svc, readiness)
	// Field for field the same, like the exec result.
	return gates.ServiceResult(res), err
}

// unavailableContracts is the reader served when no credential is set.
type unavailableContracts struct {
	err error
}

func (u unavailableContracts) Read(context.Context, string) (domain.ContractSource, error) {
	return domain.ContractSource{}, u.err
}

// unavailableClaims is the claimer served when no credential is set.
type unavailableClaims struct {
	err error
}

func (u unavailableClaims) Claim(context.Context, domain.ClaimRequest) (domain.Claim, error) {
	return domain.Claim{}, u.err
}

func (u unavailableClaims) Release(context.Context, domain.Claim) error {
	return u.err
}

// unavailablePlan is the cloner and issue reader served when no credential
// is set.
type unavailablePlan struct {
	err error
}

func (u unavailablePlan) Clone(context.Context, string, string) (mcp.Checkout, error) {
	return nil, u.err
}

func (u unavailablePlan) CloneBranch(context.Context, string, string, string) (mcp.Checkout, error) {
	return nil, u.err
}

func (u unavailablePlan) Read(context.Context, string, int) (domain.Issue, error) {
	return domain.Issue{}, u.err
}
