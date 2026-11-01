package mcp_test

import (
	"context"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// fakeContracts answers Read with a fixed source or error and records the
// repository it was asked for.
type fakeContracts struct {
	src  domain.ContractSource
	err  error
	repo string
}

func (f *fakeContracts) Read(_ context.Context, repo string) (domain.ContractSource, error) {
	f.repo = repo
	if f.err != nil {
		return domain.ContractSource{}, f.err
	}
	return f.src, nil
}
