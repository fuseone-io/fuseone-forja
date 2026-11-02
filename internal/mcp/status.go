package mcp

import (
	"context"
	"fmt"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// RunReader is what forja.status needs from a run store: one run by id,
// and every run a filter matches, sorted with domain.CompareNewestFirst.
type RunReader interface {
	Get(ctx context.Context, id string) (domain.Run, error)
	List(ctx context.Context, f domain.RunFilter) ([]domain.Run, error)
}

// StatusInput is the argument of forja.status: a run id, or none and the
// filters and page of a list. Every field is optional.
type StatusInput struct {
	RunID      string `json:"run_id,omitempty" jsonschema:"identifier of one run; omit it to list runs"`
	Repository string `json:"repository,omitempty" jsonschema:"list only the runs of this repository, in owner/name form"`
	Tool       string `json:"tool,omitempty" jsonschema:"list only the runs of this tool, such as forja.implement"`
	State      string `json:"state,omitempty" jsonschema:"list only the runs in this state: queued, running, succeeded or failed"`
	Since      string `json:"since,omitempty" jsonschema:"RFC3339 instant; list only the runs started at or after it"`
	Until      string `json:"until,omitempty" jsonschema:"RFC3339 instant; list only the runs started before it"`
	Limit      int    `json:"limit,omitempty" jsonschema:"runs per page, 1 to 200; 50 when omitted"`
	Cursor     string `json:"cursor,omitempty" jsonschema:"next_cursor of the previous page"`
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
	// ComputeMinutes is the container-minutes of the run's workspaces and
	// service sidecars, to three decimals; 0 for a run that launched none.
	// EstimatedCostUSDMicros prices the compute and the tokens at the
	// server's current price table, and is absent when it has none. For a
	// run in flight both count the stages that ended.
	ComputeMinutes         float64 `json:"compute_minutes"`
	EstimatedCostUSDMicros *int64  `json:"estimated_cost_usd_micros,omitempty"`
	// FailureReason says why a failed run failed when the control plane
	// knows more than its state; absent otherwise.
	FailureReason string `json:"failure_reason,omitempty"`
	// Stages are an implement run's steps as they started; Setup and Gates
	// the report of the gates it ran, with each raw stream's tail. All three
	// are always on the wire, empty for a run that has none.
	Stages []StageReport `json:"stages"`
	Setup  []StepReport  `json:"setup"`
	Gates  []StepReport  `json:"gates"`
	// BaseSHA, ContractSHA and HeadSHA are absent until the run knows them;
	// PullRequest until it opened one.
	BaseSHA     string          `json:"base_sha,omitempty"`
	ContractSHA string          `json:"contract_sha,omitempty"`
	HeadSHA     string          `json:"head_sha,omitempty"`
	PullRequest *PullRequestRef `json:"pull_request,omitempty"`
	// HumanMerge, ProofApparatus and HumanMergePaths are what of the agent's
	// head asks for a human merge, as forja.implement reports them; always
	// on the wire, false and empty before the head is adopted.
	HumanMerge      bool          `json:"human_merge"`
	ProofApparatus  []ChangedPath `json:"proof_apparatus"`
	HumanMergePaths []ChangedPath `json:"human_merge_paths"`
	// Verdict and VerdictReason are a verify run's, absent until it reached
	// one; Excluded the changed paths verify.exclude matched, always on the
	// wire, empty for any other run.
	Verdict       string   `json:"verdict,omitempty"`
	VerdictReason string   `json:"verdict_reason,omitempty"`
	Excluded      []string `json:"excluded"`
	// WallClockSeconds and MaxDiffLines are the limits an implement run
	// works under, absent before it read its contract; DiffLines is what its
	// head adds and deletes against the base, absent until counted.
	WallClockSeconds float64 `json:"wall_clock_seconds,omitempty"`
	MaxDiffLines     int     `json:"max_diff_lines,omitempty"`
	DiffLines        *int    `json:"diff_lines,omitempty"`
	// List is the answer to a call without a run id, and absent on one
	// with an id. When it is present every field above is its zero value.
	List *RunList `json:"list,omitempty"`
}

// StageReport is one stage of a run; FinishedAt is absent while it runs.
// Its compute and tokens are on the wire once it ended, and 0 while it
// runs or when it used none.
type StageReport struct {
	Name           string     `json:"name"`
	StartedAt      time.Time  `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	Outcome        string     `json:"outcome"`
	ComputeMinutes float64    `json:"compute_minutes"`
	TokensIn       int64      `json:"tokens_in"`
	TokensOut      int64      `json:"tokens_out"`
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
		Description: "With run_id: state, start and end of a run, the model tokens and compute minutes it used with their estimated cost, what its workspace was refused on the way out, and every request it made to its registry proxy. " +
			"Without run_id: the runs that match the filters, newest first, one page at a time, under list, with the per-repository totals of every matching run; the run fields outside list are then empty.",
		Annotations: readClass(),
	}
}

func statusHandler(runs RunReader, prices *domain.PriceTable) sdk.ToolHandlerFor[StatusInput, StatusOutput] {
	return func(ctx context.Context, _ *sdk.CallToolRequest, in StatusInput) (*sdk.CallToolResult, StatusOutput, error) {
		out, err := status(ctx, runs, prices, in)
		if err != nil {
			return nil, StatusOutput{}, fmt.Errorf("%s: %w", domain.ToolStatus, err)
		}
		return nil, out, nil
	}
}

// status answers one run by id, or the list when no id is given; an id
// with any argument of the list is refused, naming that argument.
func status(ctx context.Context, runs RunReader, prices *domain.PriceTable, in StatusInput) (StatusOutput, error) {
	if in.RunID == "" {
		list, err := listRuns(ctx, runs, prices, in)
		if err != nil {
			return StatusOutput{}, err
		}
		return StatusOutput{List: &list}, nil
	}
	if arg := listArgument(in); arg != "" {
		return StatusOutput{}, fmt.Errorf("run_id cannot be combined with %s", arg)
	}
	run, err := runs.Get(ctx, in.RunID)
	if err != nil {
		return StatusOutput{}, err
	}
	return statusOutput(run, prices), nil
}

func statusOutput(run domain.Run, prices *domain.PriceTable) StatusOutput {
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

		TokensIn:               run.Tokens.In(),
		TokensOut:              run.Tokens.Output,
		ComputeMinutes:         computeMinutes(run.Compute),
		EstimatedCostUSDMicros: estimate(prices, run),
		FailureReason:          string(run.Reason),

		Stages:      stageReports(run.Stages),
		Setup:       stepReports(run.Gates.Setup),
		Gates:       stepReports(run.Gates.Gates),
		BaseSHA:     run.BaseSHA,
		ContractSHA: run.ContractSHA,
		HeadSHA:     run.HeadSHA,
		PullRequest: pullRequestRef(run.PullRequest),

		HumanMerge: run.Proof.HumanMerge(), ProofApparatus: changedPaths(run.Proof.ProofPaths), HumanMergePaths: changedPaths(run.Proof.HumanMergePaths),

		Verdict:       string(run.QA.Verdict),
		VerdictReason: string(run.QA.Reason),
		Excluded:      excludedOf(run.QA),

		WallClockSeconds: run.Limits.WallClock.Seconds(), MaxDiffLines: run.Limits.MaxDiffLines, DiffLines: run.DiffLines,
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
