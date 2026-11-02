package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/github"
	forja "github.com/fuseone-io/fuseone-forja/internal/mcp"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

const implementIssue = 12

// callLog records, in order, what the handler did to its collaborators.
type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *callLog) add(call string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, call)
}

func (l *callLog) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

// sequence hands out run-1, run-2, ...
type sequence struct {
	mu sync.Mutex
	n  int
}

func (s *sequence) next() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	return fmt.Sprintf("run-%d", s.n)
}

// fakeClaims is a Claimer that grants every claim with the request's
// coordinates, or refuses with claimErr, and records what it was asked.
type fakeClaims struct {
	calls      *callLog
	claimErr   error
	releaseErr error
	// supersedes is copied into every granted claim.
	supersedes *domain.ClaimSummary

	mu        sync.Mutex
	requests  []domain.ClaimRequest
	released  []domain.Claim
	deadlines []time.Time
}

func (f *fakeClaims) Claim(_ context.Context, req domain.ClaimRequest) (domain.Claim, error) {
	f.calls.add("claim")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	if f.claimErr != nil {
		return domain.Claim{}, f.claimErr
	}
	return domain.Claim{
		RunID: req.RunID, Repository: req.Repository, Issue: req.Issue, SHA: "sha-" + req.RunID,
		CreatedAt: startedAt(), ExpiresAt: startedAt().Add(req.Lease), Supersedes: f.supersedes,
	}, nil
}

// Release fails on a context that already ended, as the real one does
// before it reaches GitHub.
func (f *fakeClaims) Release(ctx context.Context, c domain.Claim) error {
	f.calls.add("release")
	f.mu.Lock()
	defer f.mu.Unlock()
	dl, _ := ctx.Deadline()
	f.deadlines = append(f.deadlines, dl)
	if err := ctx.Err(); err != nil {
		return err
	}
	f.released = append(f.released, c)
	return f.releaseErr
}

func (f *fakeClaims) releaseDeadline() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.deadlines) == 0 {
		return time.Time{}
	}
	return f.deadlines[0]
}

func (f *fakeClaims) requested() []domain.ClaimRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.ClaimRequest(nil), f.requests...)
}

func (f *fakeClaims) releasedClaims() []domain.Claim {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.Claim(nil), f.released...)
}

// recordingRuns is the real in-memory store with every Put that changes a
// run's state logged by the state it recorded; the Puts that only record
// a stage's progress are not logged.
type recordingRuns struct {
	*runstore.Memory
	calls *callLog

	mu   sync.Mutex
	last map[string]domain.RunState
}

func (r *recordingRuns) Put(ctx context.Context, run domain.Run) error {
	r.mu.Lock()
	if r.last == nil {
		r.last = map[string]domain.RunState{}
	}
	prev, seen := r.last[run.ID]
	r.last[run.ID] = run.State
	r.mu.Unlock()
	if !seen || prev != run.State {
		r.calls.add("put " + string(run.State))
	}
	return r.Memory.Put(ctx, run)
}

// failingRuns is the recording store with every Put of a run in failState
// refused.
type failingRuns struct {
	*recordingRuns
	failState domain.RunState
}

func (r *failingRuns) Put(ctx context.Context, run domain.Run) error {
	if run.State == r.failState {
		r.calls.add("put " + string(run.State))
		return errors.New("run store down")
	}
	return r.recordingRuns.Put(ctx, run)
}

// implementFixture is a server wired with a granting claimer over a valid
// contract with wall_clock 30m, a base checkout, an issue, an agent that
// exits 0 with a commit, green gates, and a push, a pull request and a
// status that all succeed.
type implementFixture struct {
	calls    *callLog
	claims   *fakeClaims
	runs     *recordingRuns
	agent    *fakeAgent
	cloner   *fakeCloner
	issues   *fakeIssues
	gates    *fakeGates
	pusher   *fakePusher
	prs      *fakePullRequests
	statuses *fakeStatuses
	cs       *sdk.ClientSession
}

func newImplementFixture(t *testing.T, contract []byte, mutate func(*fakeClaims)) *implementFixture {
	t.Helper()
	return newAgentFixture(t, contract, mutate, &fakeAgent{result: exported()})
}

// newAgentFixture is the fixture with the given agent.
func newAgentFixture(t *testing.T, contract []byte, mutate func(*fakeClaims), agent *fakeAgent) *implementFixture {
	t.Helper()
	return newWiredFixture(t, contract, fixtureWiring{claims: mutate, agent: agent})
}

// fixtureWiring changes the fixture before the server is connected: the
// claimer, the agent, and any other dependency.
type fixtureWiring struct {
	claims func(*fakeClaims)
	agent  *fakeAgent
	deps   func(*forja.Deps)
}

func newWiredFixture(t *testing.T, contract []byte, w fixtureWiring) *implementFixture {
	t.Helper()
	calls := &callLog{}
	claims := &fakeClaims{calls: calls}
	if w.claims != nil {
		w.claims(claims)
	}
	agent := w.agent
	if agent == nil {
		agent = &fakeAgent{result: exported()}
	}
	agent.calls = calls
	runs := &recordingRuns{Memory: runstore.NewMemory(), calls: calls}
	contracts := &fakeContracts{src: domain.ContractSource{SHA: doctorSHA, Body: contract}}
	deps := testDeps(runs, contracts, claims)
	fx := &implementFixture{
		calls: calls, claims: claims, runs: runs, agent: agent,
		cloner: &fakeCloner{co: implementCheckout(), log: calls}, issues: &fakeIssues{issue: implementStatement},
		gates:    &fakeGates{report: greenReport(), log: calls},
		pusher:   &fakePusher{deliveryLog[github.PushRequest]{calls: calls, name: "push"}},
		prs:      &fakePullRequests{deliveryLog[github.PullRequestRequest]{calls: calls, name: "pull_request"}},
		statuses: &fakeStatuses{deliveryLog[github.StatusRequest]{calls: calls, name: "status"}},
	}
	deps.Cloner, deps.Issues, deps.Agent, deps.Gates = fx.cloner, fx.issues, agent, fx.gates
	deps.Pusher, deps.PullRequests, deps.Statuses = fx.pusher, fx.prs, fx.statuses
	if w.deps != nil {
		w.deps(&deps)
	}
	fx.cs = connectDeps(t, deps)
	return fx
}

var contractWithWallClock = []byte("version: 1\nimage: golang:1.25\ngates:\n  - name: build\n    run: make build\nlimits:\n  wall_clock: 30m\n")

func callImplement(t *testing.T, cs *sdk.ClientSession, issue int) *sdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      string(domain.ToolImplement),
		Arguments: map[string]any{"repository": doctorRepo, "issue": issue, "base_branch": "main"},
	})
	if err != nil {
		t.Fatalf("CallTool(forja.implement): protocol error %v", err)
	}
	return res
}

func decodeImplement(t *testing.T, res *sdk.CallToolResult) forja.ImplementOutput {
	t.Helper()
	if res.IsError {
		t.Fatalf("IsError = true, content: %s", textOf(res))
	}
	var out forja.ImplementOutput
	if err := json.Unmarshal(structuredJSON(t, res), &out); err != nil {
		t.Fatalf("decode ImplementOutput: %v", err)
	}
	return out
}

func TestImplement_free_claimsRecordsRunsAgentReleasesThenTerminal(t *testing.T) {
	t.Parallel()
	fx := newImplementFixture(t, contractWithWallClock, nil)

	out := decodeImplement(t, callImplement(t, fx.cs, implementIssue))
	want := []string{"claim", "put running", "clone", "agent", "gates", "push", "pull_request", "status", "release", "put succeeded"}
	if got := fx.calls.list(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	assertSucceededRun(t, fx.runs.Memory, out.RunID)
	released := fx.claims.releasedClaims()
	if len(released) != 1 || released[0].RunID != out.RunID || released[0].SHA != "sha-"+out.RunID {
		t.Fatalf("released = %+v, want the claim granted to %s", released, out.RunID)
	}
}

// assertSucceededRun checks the store holds a finished, succeeded
// forja.implement run on the issue that superseded nothing.
func assertSucceededRun(t *testing.T, store *runstore.Memory, runID string) {
	t.Helper()
	run, err := store.Get(context.Background(), runID)
	if err != nil {
		t.Fatalf("the run %q the output names is not in the store: %v", runID, err)
	}
	if run.State != domain.StateSucceeded || run.FinishedAt == nil || run.Tool != domain.ToolImplement {
		t.Fatalf("run = %+v, want a finished, succeeded forja.implement run", run)
	}
	if run.Repository != doctorRepo || run.Issue != implementIssue || run.Supersedes != "" {
		t.Fatalf("run = %+v, want issue %d of %s with nothing superseded", run, implementIssue, doctorRepo)
	}
}

func TestImplement_held_toolErrorNamesHolderRunIDAndRecordsNoRun(t *testing.T) {
	t.Parallel()
	holder := domain.Claim{RunID: "run-holder", Repository: doctorRepo, Issue: implementIssue, ExpiresAt: startedAt().Add(45 * time.Minute)}
	fx := newImplementFixture(t, contractWithWallClock, func(f *fakeClaims) {
		f.claimErr = &domain.ClaimHeldError{Holder: holder}
	})

	res := callImplement(t, fx.cs, implementIssue)
	if !res.IsError {
		t.Fatalf("IsError = false for a held issue: %s", textOf(res))
	}
	text := textOf(res)
	for _, want := range []string{string(domain.ToolImplement), "run-holder"} {
		if !strings.Contains(text, want) {
			t.Errorf("error text %q does not contain %q", text, want)
		}
	}
	if got, want := fx.calls.list(), []string{"claim"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q, want only %q: no run recorded, nothing released", got, want)
	}
	if _, err := fx.runs.Get(context.Background(), "run-1"); !errors.Is(err, domain.ErrRunNotFound) {
		t.Fatalf("a refused claim left a run in the store: %v", err)
	}
}

func TestImplement_releaseFails_runFailedAndErrorReturned(t *testing.T) {
	t.Parallel()
	fx := newImplementFixture(t, contractWithWallClock, func(f *fakeClaims) {
		f.releaseErr = fmt.Errorf("release issue 12: %w", domain.ErrClaimLost)
	})

	res := callImplement(t, fx.cs, implementIssue)
	if !res.IsError {
		t.Fatalf("IsError = false when the release fails: %s", textOf(res))
	}
	text := textOf(res)
	for _, want := range []string{"claim lost", "run-1"} {
		if !strings.Contains(text, want) {
			t.Errorf("error text %q does not contain %q", text, want)
		}
	}
	want := []string{"claim", "put running", "clone", "agent", "gates", "push", "pull_request", "status", "release", "put failed"}
	if got := fx.calls.list(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	run, err := fx.runs.Get(context.Background(), "run-1")
	if err != nil || run.State != domain.StateFailed || run.FinishedAt == nil {
		t.Fatalf("run = %+v, %v; want failed and finished", run, err)
	}
}

// A claim is held only while a run is recorded against it: when the running
// record cannot be written, the ref comes off before the error goes back.
func TestImplement_recordRunningFails_claimStillReleased(t *testing.T) {
	t.Parallel()
	calls := &callLog{}
	claims := &fakeClaims{calls: calls}
	runs := &failingRuns{recordingRuns: &recordingRuns{Memory: runstore.NewMemory(), calls: calls}, failState: domain.StateRunning}
	contracts := &fakeContracts{src: domain.ContractSource{SHA: doctorSHA, Body: contractWithWallClock}}
	cs := connectDeps(t, testDeps(runs, contracts, claims))

	res := callImplement(t, cs, implementIssue)
	if !res.IsError || !strings.Contains(textOf(res), "run store down") {
		t.Fatalf("IsError = %v, text %q; want a tool error carrying the store failure", res.IsError, textOf(res))
	}
	want := []string{"claim", "put running", "release"}
	if got := calls.list(); !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %q, want %q: the claim is released even though no run was recorded", got, want)
	}
	if released := claims.releasedClaims(); len(released) != 1 || released[0].RunID != "run-1" {
		t.Fatalf("released = %+v, want the claim granted to run-1", released)
	}
}

func TestImplement_leaseComesFromContractWallClock(t *testing.T) {
	t.Parallel()
	fx := newImplementFixture(t, contractWithWallClock, nil)

	decodeImplement(t, callImplement(t, fx.cs, implementIssue))
	reqs := fx.claims.requested()
	if len(reqs) != 1 {
		t.Fatalf("claims requested = %+v, want one", reqs)
	}
	want := domain.ClaimRequest{Repository: doctorRepo, Issue: implementIssue, RunID: "run-1", Lease: 45 * time.Minute}
	if reqs[0] != want {
		t.Fatalf("ClaimRequest = %+v, want %+v (wall_clock 30m + 15m)", reqs[0], want)
	}
}

// A configured lease replaces the default when it outlives the wall clock
// and is a tool error, with no claim, when it does not.
func TestImplement_configuredLease_winsOrIsRefused(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		lease     time.Duration
		wantLease time.Duration
		wantErr   bool
	}{
		{name: "longer than wall_clock", lease: 50 * time.Minute, wantLease: 50 * time.Minute},
		{name: "shorter than wall_clock", lease: 20 * time.Minute, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			calls := &callLog{}
			claims := &fakeClaims{calls: calls}
			contracts := &fakeContracts{src: domain.ContractSource{SHA: doctorSHA, Body: contractWithWallClock}}
			deps := testDeps(&recordingRuns{Memory: runstore.NewMemory(), calls: calls}, contracts, claims)
			deps.Lease = tt.lease
			deps.Cloner, deps.Issues, deps.Agent = &fakeCloner{co: implementCheckout()}, &fakeIssues{issue: implementStatement}, &fakeAgent{calls: calls}
			cs := connectDeps(t, deps)

			res := callImplement(t, cs, implementIssue)
			if tt.wantErr {
				if !res.IsError || !strings.Contains(textOf(res), "20m") {
					t.Fatalf("IsError = %v, text %q; want a tool error naming the lease", res.IsError, textOf(res))
				}
				if len(claims.requested()) != 0 {
					t.Fatalf("a refused lease still claimed: %+v", claims.requested())
				}
				return
			}
			decodeImplement(t, res)
			if reqs := claims.requested(); len(reqs) != 1 || reqs[0].Lease != tt.wantLease {
				t.Fatalf("requested = %+v, want one claim with lease %v", reqs, tt.wantLease)
			}
		})
	}
}

func TestImplement_contractInvalidOrUnreadable_toolErrorAndNoClaim(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		contracts *fakeContracts
		wantText  string
	}{
		{name: "invalid contract", contracts: &fakeContracts{src: domain.ContractSource{SHA: doctorSHA, Body: invalidContract}}, wantText: "gates[0].run"},
		{name: "reader fails", contracts: &fakeContracts{err: errors.New("no GitHub credentials")}, wantText: "no GitHub credentials"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			calls := &callLog{}
			claims := &fakeClaims{calls: calls}
			cs := connectDeps(t, testDeps(&recordingRuns{Memory: runstore.NewMemory(), calls: calls}, tt.contracts, claims))

			res := callImplement(t, cs, implementIssue)
			if !res.IsError {
				t.Fatalf("IsError = false: %s", textOf(res))
			}
			if !strings.Contains(textOf(res), tt.wantText) {
				t.Fatalf("error text %q does not contain %q", textOf(res), tt.wantText)
			}
			if got := calls.list(); len(got) != 0 {
				t.Fatalf("calls = %q, want none before the contract is accepted", got)
			}
		})
	}
}

func TestImplement_issueNotPositive_toolErrorBeforeAnyCall(t *testing.T) {
	t.Parallel()
	fx := newImplementFixture(t, contractWithWallClock, nil)

	res := callImplement(t, fx.cs, 0)
	if !res.IsError || !strings.Contains(textOf(res), "issue") {
		t.Fatalf("IsError = %v, text %q; want a tool error naming the issue", res.IsError, textOf(res))
	}
	if got := fx.calls.list(); len(got) != 0 {
		t.Fatalf("calls = %q, want none", got)
	}
}

func TestImplement_supersedes_visibleInStatus(t *testing.T) {
	t.Parallel()
	fx := newImplementFixture(t, contractWithWallClock, func(f *fakeClaims) {
		f.supersedes = &domain.ClaimSummary{RunID: "run-expired", SHA: "sha-expired", ExpiresAt: startedAt().Add(-time.Minute)}
	})

	out := decodeImplement(t, callImplement(t, fx.cs, implementIssue))
	res := callStatus(t, fx.cs, map[string]any{"run_id": out.RunID})
	if res.IsError {
		t.Fatalf("status IsError = true: %s", textOf(res))
	}
	wire := wireOf(t, res)
	raw, present := wire["supersedes"]
	if !present || string(raw) != `"run-expired"` {
		t.Fatalf("supersedes on the wire = %s (present %v), want \"run-expired\"", raw, present)
	}
	if len(wire) != 22 {
		t.Fatalf("wire object has %d keys, want the 21 of a delivered run plus supersedes: %s", len(wire), structuredJSON(t, res))
	}
}

// deadlineRuns keeps the deadline of every Put.
type deadlineRuns struct {
	forja.RunStore

	mu        sync.Mutex
	deadlines []time.Time
}

func (r *deadlineRuns) Put(ctx context.Context, run domain.Run) error {
	dl, _ := ctx.Deadline()
	r.mu.Lock()
	r.deadlines = append(r.deadlines, dl)
	r.mu.Unlock()
	return r.RunStore.Put(ctx, run)
}

func (r *deadlineRuns) first() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.deadlines[0]
}

// The release is bounded from when it starts, not from when the run
// started: a run that works longer than the release's bound still frees
// its issue.
func TestImplement_runOutlastsFinishBound_releaseStillHasItsOwn(t *testing.T) {
	t.Parallel()
	var runs *deadlineRuns
	fx := newWiredFixture(t, contractWithWallClock, fixtureWiring{deps: func(d *forja.Deps) {
		runs = &deadlineRuns{RunStore: d.Runs}
		d.Runs = runs
	}})
	const worked = 20 * time.Millisecond
	fx.gates.during = func() { time.Sleep(worked) }

	decodeImplement(t, callImplement(t, fx.cs, implementIssue))
	first, release := runs.first(), fx.claims.releaseDeadline()
	if first.IsZero() || release.Sub(first) < worked {
		t.Fatalf("release deadline %v, first record's %v: the release's bound was spent while the run worked %v", release, first, worked)
	}
}

func TestStatus_supersedes_absentWhenEmpty(t *testing.T) {
	t.Parallel()
	store := runstore.NewMemory()
	mustPut(t, store, terminalRun("run-plain"))
	cs := connect(t, store)

	res := callStatus(t, cs, map[string]any{"run_id": "run-plain"})
	if _, present := wireOf(t, res)["supersedes"]; present {
		t.Fatalf("supersedes present on the wire for a run that superseded nothing: %s", structuredJSON(t, res))
	}
}
