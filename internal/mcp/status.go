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
	// RegistryAccessCount is every request the run's registry proxy served
	// or refused; RegistryAccess lists the first of them, empty when the
	// run made none or declares no registry.
	RegistryAccessCount int             `json:"registry_access_count"`
	RegistryAccess      []RegistryEvent `json:"registry_access"`
	// TokensIn is every input token the run's model proxy metered, cache
	// fields included; TokensOut every output token. Both are 0 for a run
	// that called no model.
	TokensIn  int64 `json:"tokens_in"`
	TokensOut int64 `json:"tokens_out"`
	// FailureReason says why a failed run failed when the control plane
	// knows more than its state; absent otherwise.
	FailureReason string `json:"failure_reason,omitempty"`
	// Stages are an implement run's steps as they started; Setup and Gates
	// the report of the gates it ran, with each raw stream's tail. All three
	// are always on the wire, empty for a run that has none.
	Stages []StageReport `json:"stages"`
	Setup  []StepReport  `json:"setup"`
	Gates  []StepReport  `json:"gates"`
	// Attempt is the number of the attempt in flight, or of the last one,
	// 0 before the first agent stage; MaxAttempts is the base contract's
	// limit, 0 until the contract is read. Attempts lists every attempt
	// whose gates ran, always on the wire.
	Attempt     int             `json:"attempt"`
	MaxAttempts int             `json:"max_attempts"`
	Attempts    []AttemptReport `json:"attempts"`
	// BaseSHA, ContractSHA and HeadSHA are absent until the run knows them;
	// PullRequest until it opened one.
	BaseSHA     string          `json:"base_sha,omitempty"`
	ContractSHA string          `json:"contract_sha,omitempty"`
	HeadSHA     string          `json:"head_sha,omitempty"`
	PullRequest *PullRequestRef `json:"pull_request,omitempty"`
}

// StageReport is one stage of a run; FinishedAt is absent while it runs,
// and Attempt for a stage outside every attempt.
type StageReport struct {
	Name       string     `json:"name"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Outcome    string     `json:"outcome"`
	Attempt    int        `json:"attempt,omitempty"`
}

// RegistryEvent is one request the workspace made to its registry proxy.
type RegistryEvent struct {
	At       time.Time `json:"at"`
	Registry string    `json:"registry"`
	// Path follows the registry entry, without the query.
	Path    string `json:"path"`
	Outcome string `json:"outcome"`
	Status  int    `json:"status"`
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
		Description: "State, start and end of a run, its stages, the attempt in flight out of the contract's limit with every attempt's gates, the model tokens it used, what its workspace was refused on the way out, and every request it made to its registry proxy.",
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

		RegistryAccessCount: run.Registry.Count,
		RegistryAccess:      registryEvents(run.Registry.Accesses),

		TokensIn:      run.Tokens.In(),
		TokensOut:     run.Tokens.Output,
		FailureReason: string(run.Reason),

		Stages:      stageReports(run.Stages),
		Setup:       stepReports(run.Gates.Setup),
		Gates:       stepReports(run.Gates.Gates),
		Attempt:     currentAttempt(run.Stages),
		MaxAttempts: run.MaxAttempts,
		Attempts:    attemptReports(run.Attempts),
		BaseSHA:     run.BaseSHA,
		ContractSHA: run.ContractSHA,
		HeadSHA:     run.HeadSHA,
		PullRequest: pullRequestRef(run.PullRequest),
	}
}

func registryEvents(accesses []domain.RegistryAccess) []RegistryEvent {
	events := make([]RegistryEvent, 0, len(accesses))
	for _, a := range accesses {
		events = append(events, RegistryEvent{At: a.At, Registry: a.Registry, Path: a.Path, Outcome: string(a.Outcome), Status: a.Status})
	}
	return events
}

func egressEvents(denied []domain.EgressDenial) []EgressEvent {
	events := make([]EgressEvent, 0, len(denied))
	for _, d := range denied {
		events = append(events, EgressEvent{At: d.At, Kind: string(d.Kind), Destination: d.Destination})
	}
	return events
}
