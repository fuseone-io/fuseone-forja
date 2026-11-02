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
	// Supersedes names the run whose expired claim this run took over;
	// absent when the issue was free.
	Supersedes string `json:"supersedes,omitempty"`
	// EgressDeniedCount is every attempt the run's gateway refused;
	// EgressDenied lists the first of them, empty when there were none.
	EgressDeniedCount int           `json:"egress_denied_count"`
	EgressDenied      []EgressEvent `json:"egress_denied"`
}

// EgressEvent is one refused attempt to leave the workspace.
type EgressEvent struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
	// Destination is host:port only.
	Destination string `json:"destination"`
}

func statusTool() *sdk.Tool {
	return &sdk.Tool{
		Name:        string(domain.ToolStatus),
		Description: "State, start and end of a run, and what its workspace was refused on the way out.",
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
		Supersedes: run.Supersedes,

		EgressDeniedCount: run.Egress.Count,
		EgressDenied:      egressEvents(run.Egress.Denied),
	}
}

func egressEvents(denied []domain.EgressDenial) []EgressEvent {
	events := make([]EgressEvent, 0, len(denied))
	for _, d := range denied {
		events = append(events, EgressEvent{At: d.At, Kind: string(d.Kind), Destination: d.Destination})
	}
	return events
}
