package mcp

import (
	"context"
	"errors"
	"fmt"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/contract"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// ContractReader is what forja.doctor needs from GitHub: the contract at
// the commit of the default branch, with that commit's SHA.
type ContractReader interface {
	Read(ctx context.Context, repo string) (domain.ContractSource, error)
}

// DoctorInput is the argument of forja.doctor.
type DoctorInput struct {
	Repository string `json:"repository" jsonschema:"repository in owner/name form"`
}

// DoctorError is one defect of the contract, as the maintainer fixes it.
type DoctorError struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Message string `json:"message"`
}

// DoctorOutput is the result of forja.doctor: the commit the contract was
// read at and either the normalised contract or its defects. Errors is
// always on the wire, empty when the contract is accepted.
type DoctorOutput struct {
	Repository string        `json:"repository"`
	SHA        string        `json:"sha"`
	OK         bool          `json:"ok"`
	Errors     []DoctorError `json:"errors"`
	// Contract is absent when the contract was rejected.
	Contract *domain.Contract `json:"contract,omitempty"`
}

func doctorTool() *sdk.Tool {
	return &sdk.Tool{
		Name:        string(domain.ToolDoctor),
		Description: "Validate the repository's .forja.yaml at the commit of its default branch, with no push.",
		Annotations: readClass(),
	}
}

// doctorHandler reads and validates the contract. A reader failure,
// including a missing contract, is a tool error; a contract with defects
// is a result the maintainer iterates on.
func doctorHandler(contracts ContractReader) sdk.ToolHandlerFor[DoctorInput, DoctorOutput] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, in DoctorInput) (*sdk.CallToolResult, DoctorOutput, error) {
		src, err := contracts.Read(ctx, in.Repository)
		if err != nil {
			return nil, DoctorOutput{}, fmt.Errorf("%s: %w", domain.ToolDoctor, err)
		}
		return nil, doctorOutput(in.Repository, src), nil
	}
}

func doctorOutput(repo string, src domain.ContractSource) DoctorOutput {
	out := DoctorOutput{Repository: repo, SHA: src.SHA, Errors: []DoctorError{}}
	parsed, err := contract.Parse(src.Body)
	var defects contract.Errors
	if errors.As(err, &defects) {
		for _, d := range defects {
			out.Errors = append(out.Errors, DoctorError{Path: d.Path, Line: d.Line, Message: d.Message})
		}
		return out
	}
	out.OK = true
	out.Contract = &parsed
	return out
}
