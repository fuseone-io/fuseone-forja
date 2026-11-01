package mcp

import (
	"context"
	"errors"
	"fmt"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/contract"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/gates"
)

// GateRunner is what forja.doctor needs to run a contract: setup and gates
// against a tree, with every exit code and raw log in the report.
type GateRunner interface {
	Run(ctx context.Context, c domain.Contract, tree string) (domain.GateReport, error)
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
// read at and the gates ran on, then either the contract's defects or the
// normalised contract with the report of its run. OK is true only when the
// contract parsed and every step that had to run passed. Every list is
// always on the wire, empty when there is nothing to say.
type DoctorOutput struct {
	Repository string `json:"repository"`
	SHA        string `json:"sha"`
	// ContractSHA is the blob SHA of .forja.yaml at SHA, as forja.plan
	// reports it.
	ContractSHA string        `json:"contract_sha"`
	OK          bool          `json:"ok"`
	Errors      []DoctorError `json:"errors"`
	// Contract is absent when the contract was rejected.
	Contract *domain.Contract `json:"contract,omitempty"`
	Setup    []StepReport     `json:"setup"`
	Gates    []StepReport     `json:"gates"`
	// FailedStep names the step that made the repository not ready, absent
	// when none did.
	FailedStep string   `json:"failed_step,omitempty"`
	Warnings   []string `json:"warnings"`
}

func doctorTool() *sdk.Tool {
	return &sdk.Tool{
		Name:        string(domain.ToolDoctor),
		Description: "Validate the repository's .forja.yaml at its default branch and run its setup and gates there, with no push.",
		Annotations: readClass(),
	}
}

// doctorHandler clones the default branch, validates the contract at that
// commit and runs it on that tree. A clone, read or runner failure is a
// tool error; a contract with defects or a red gate is a result the
// maintainer iterates on.
func doctorHandler(cloner Cloner, runner GateRunner) sdk.ToolHandlerFor[DoctorInput, DoctorOutput] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, in DoctorInput) (*sdk.CallToolResult, DoctorOutput, error) {
		out, err := doctor(ctx, cloner, runner, in.Repository)
		if err != nil {
			return nil, DoctorOutput{}, fmt.Errorf("%s: %w", domain.ToolDoctor, err)
		}
		return nil, out, nil
	}
}

func doctor(ctx context.Context, cloner Cloner, runner GateRunner, repo string) (DoctorOutput, error) {
	co, err := cloner.Clone(ctx, repo, "")
	if err != nil {
		return DoctorOutput{}, err
	}
	// The result is the report; a clone directory that could not be removed
	// changes none of it, so that failure is not the tool's.
	defer func() { _ = co.Close() }()
	parsed, contractSHA, err := readContract(ctx, co, repo)
	out := emptyDoctorOutput(repo, co.SHA(), contractSHA)
	var defects contract.Errors
	switch {
	case errors.As(err, &defects):
		out.Errors = doctorErrors(defects)
		return out, nil
	case err != nil:
		return DoctorOutput{}, err
	}
	report, err := runGates(ctx, runner, parsed, co.Dir())
	if err != nil {
		return DoctorOutput{}, err
	}
	out.Contract = &parsed
	out.addReport(report)
	return out, nil
}

// runGates runs the contract under its own wall clock, or the default
// when the contract sets none.
func runGates(ctx context.Context, runner GateRunner, c domain.Contract, tree string) (domain.GateReport, error) {
	wallClock := c.Limits.WallClock
	if wallClock == 0 {
		wallClock = gates.DefaultWallClock
	}
	runCtx, cancel := context.WithTimeout(ctx, wallClock)
	defer cancel()
	return runner.Run(runCtx, c, tree)
}

func emptyDoctorOutput(repo, sha, contractSHA string) DoctorOutput {
	return DoctorOutput{
		Repository:  repo,
		SHA:         sha,
		ContractSHA: contractSHA,
		Errors:      []DoctorError{},
		Setup:       []StepReport{},
		Gates:       []StepReport{},
		Warnings:    []string{},
	}
}

func doctorErrors(defects contract.Errors) []DoctorError {
	out := make([]DoctorError, 0, len(defects))
	for _, d := range defects {
		out = append(out, DoctorError{Path: d.Path, Line: d.Line, Message: d.Message})
	}
	return out
}
