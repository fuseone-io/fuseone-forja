package mcp

import (
	"context"
	"fmt"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// RunReader is what forja.status needs from a run store.
type RunReader interface {
	Get(ctx context.Context, id string) (domain.Run, error)
}

// StatusInput is the argument of forja.status.
type StatusInput struct {
	RunID string `json:"run_id" jsonschema:"identifier of the run"`
}

// StatusOutput is the wire form of a run as forja.status reports it.
type StatusOutput struct {
	ID         string    `json:"id"`
	Tool       string    `json:"tool"`
	Repository string    `json:"repository"`
	Issue      int       `json:"issue"`
	State      string    `json:"state"`
	StartedAt  time.Time `json:"started_at"`
	// FinishedAt is absent while the run is not terminal.
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

func statusTool() *sdk.Tool {
	return &sdk.Tool{
		Name:        string(domain.ToolStatus),
		Description: "State, start and end of a run.",
		Annotations: readClass(),
	}
}

func statusHandler(runs RunReader) sdk.ToolHandlerFor[StatusInput, StatusOutput] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, in StatusInput) (*sdk.CallToolResult, StatusOutput, error) {
		run, err := runs.Get(ctx, in.RunID)
		if err != nil {
			return nil, StatusOutput{}, fmt.Errorf("%s: %w", domain.ToolStatus, err)
		}
		return nil, statusOutput(run), nil
	}
}

func statusOutput(run domain.Run) StatusOutput {
	return StatusOutput{
		ID:         run.ID,
		Tool:       string(run.Tool),
		Repository: run.Repository,
		Issue:      run.Issue,
		State:      string(run.State),
		StartedAt:  run.StartedAt,
		FinishedAt: run.FinishedAt,
	}
}
