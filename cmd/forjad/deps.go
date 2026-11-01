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
	"github.com/fuseone-io/fuseone-forja/internal/github"
	"github.com/fuseone-io/fuseone-forja/internal/mcp"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
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
// and the others answer with the error naming the variables to set; the
// claim store is then nil. A half-configured App or a malformed lease is a
// startup failure. The wall clock and the id generator are injected here,
// at the edge.
func wire(getenv func(string) string, stderr io.Writer) (mcp.Deps, *claim.Store, error) {
	deps := mcp.Deps{Runs: runstore.NewMemory(), NewID: newRunID, Now: time.Now, Version: version}
	lease, err := claimLease(getenv)
	if err != nil {
		return mcp.Deps{}, nil, err
	}
	deps.Lease = lease
	tokens, err := github.TokenSourceFromEnv(getenv)
	if errors.Is(err, github.ErrNoCredentials) {
		deps.Contracts = unavailableContracts{err: err}
		deps.Claims = unavailableClaims{err: err}
		return deps, nil, nil
	}
	if err != nil {
		return mcp.Deps{}, nil, err
	}
	deps.Contracts = &github.ContractReader{Tokens: tokens}
	store := claim.NewStore(&github.Refs{Tokens: tokens}, time.Now, journal(getenv, stderr))
	deps.Claims = store
	return deps, store, nil
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

// newRunID is 16 random bytes in hex. rand.Read never returns an error:
// it fills the buffer or stops the process.
func newRunID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
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
