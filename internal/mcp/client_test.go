package mcp_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	forja "github.com/fuseone-io/fuseone-forja/internal/mcp"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

const (
	clientA = "client-a"
	clientB = "client-b"
)

// restricted is a client that reaches only repos.
func restricted(name string, repos ...string) domain.Client {
	return domain.Client{Name: name, Repositories: repos}
}

// connectAs is connect with every call coming from client.
func connectAs(t *testing.T, store forja.RunStore, client domain.Client) *sdk.ClientSession {
	t.Helper()
	deps := testDeps(store, &fakeContracts{err: errors.New("no contract reader in this test")}, &fakeClaims{calls: &callLog{}, claimErr: errors.New("no claimer in this test")})
	deps.Callers = fixedCallers{client: client}
	return connectDeps(t, deps)
}

// tripwire is a server whose every collaborator a tool reads a repository
// through records that it was reached; past the first read, each fails.
type tripwire struct {
	calls     *callLog
	contracts *fakeContracts
	claims    *fakeClaims
	cloner    *fakeCloner
	issues    *fakeIssues
	pulls     *fakePullReader
	store     *runstore.Memory
	cs        *sdk.ClientSession
}

func newTripwire(t *testing.T, callers forja.Callers) *tripwire {
	t.Helper()
	calls := &callLog{}
	tw := &tripwire{
		calls:     calls,
		contracts: &fakeContracts{err: errors.New("tripwire: contract")},
		claims:    &fakeClaims{calls: calls, claimErr: errors.New("tripwire: claim")},
		cloner:    &fakeCloner{err: errors.New("tripwire: clone"), log: calls},
		issues:    &fakeIssues{err: errors.New("tripwire: issue")},
		pulls:     &fakePullReader{calls: calls, err: errors.New("tripwire: pull request")},
		store:     runstore.NewMemory(),
	}
	deps := testDeps(tw.store, tw.contracts, tw.claims)
	deps.Cloner, deps.Issues, deps.PullRequestReader, deps.Callers = tw.cloner, tw.issues, tw.pulls, callers
	tw.cs = connectDeps(t, deps)
	return tw
}

// reached is every collaborator the server read a repository through, in
// the order the call log saw them, then the contract and the issue.
func (tw *tripwire) reached() []string {
	got := tw.calls.list()
	if tw.contracts.repo != "" {
		got = append(got, "contract "+tw.contracts.repo)
	}
	if tw.issues.repo != "" {
		got = append(got, "issue "+tw.issues.repo)
	}
	return got
}

// toolCall is the arguments of each tool that names repo.
func toolCalls(repo string) map[domain.Tool]map[string]any {
	return map[domain.Tool]map[string]any{
		domain.ToolDoctor:    {"repository": repo},
		domain.ToolPlan:      {"repository": repo, "issue": 5, "base_branch": "main"},
		domain.ToolImplement: {"repository": repo, "issue": 5, "base_branch": "main"},
		domain.ToolVerify:    {"repository": repo, "pull_request": 9},
		domain.ToolStatus:    {"repository": repo},
	}
}

func call(t *testing.T, cs *sdk.ClientSession, tool domain.Tool, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: string(tool), Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): protocol error %v", tool, err)
	}
	return res
}

// A tool naming a repository outside the client's is a tool error, and
// nothing was read for it: no contract, no claim, no clone, no issue, no
// pull request, no run.
func TestTools_repositoryOutsideTheClients_refusedBeforeAnyRead(t *testing.T) {
	t.Parallel()
	for _, repo := range []string{"evil/payments", "acme-co/payments", "acme/ledger", "acme/..", "acme/payments/x"} {
		for tool, args := range toolCalls(repo) {
			t.Run(string(tool)+" "+repo, func(t *testing.T) {
				t.Parallel()
				tw := newTripwire(t, fixedCallers{client: restricted(clientA, "acme/payments", "beta/*")})
				res := call(t, tw.cs, tool, args)
				text := textOf(res)
				if !res.IsError || !strings.Contains(text, string(tool)) || !strings.Contains(text, fmt.Sprintf("%q", repo)) || !strings.Contains(text, forja.ErrRepositoryNotReached.Error()) {
					t.Fatalf("IsError %v, text %q; want a tool error naming %s, %q and the refusal", res.IsError, text, tool, repo)
				}
				if got := tw.reached(); len(got) != 0 {
					t.Fatalf("reached %q for a repository outside the client's", got)
				}
				if runs, _ := tw.store.List(context.Background(), domain.RunFilter{}); len(runs) != 0 {
					t.Fatalf("recorded %d runs for a refused call", len(runs))
				}
			})
		}
	}
}

// A repository the client reaches, in whatever case, gets past the scope
// to the tool's first read.
func TestTools_repositoryTheClientReaches_reachesTheFirstRead(t *testing.T) {
	t.Parallel()
	first := map[domain.Tool]string{
		domain.ToolDoctor: "clone", domain.ToolPlan: "clone", domain.ToolImplement: "contract", domain.ToolVerify: "read pull request",
	}
	for _, repo := range []string{"acme/payments", "ACME/Payments", "beta/anything", "Beta/x"} {
		for tool, args := range toolCalls(repo) {
			t.Run(string(tool)+" "+repo, func(t *testing.T) {
				t.Parallel()
				tw := newTripwire(t, fixedCallers{client: restricted(clientA, "acme/payments", "beta/*")})
				res := call(t, tw.cs, tool, args)
				if strings.Contains(textOf(res), forja.ErrRepositoryNotReached.Error()) {
					t.Fatalf("refused a repository the client reaches: %s", textOf(res))
				}
				if tool == domain.ToolStatus {
					if res.IsError {
						t.Fatalf("forja.status on a reached repository: %s", textOf(res))
					}
					return
				}
				if got := strings.Join(tw.reached(), ","); !strings.HasPrefix(got, first[tool]) {
					t.Fatalf("reached %q, want %s first", got, first[tool])
				}
			})
		}
	}
}

// A call no client can be named for is refused before any read, for every
// tool, whether the repository would be reached or not.
func TestTools_noClientNamed_refusedBeforeAnyRead(t *testing.T) {
	t.Parallel()
	tests := map[string]forja.Callers{
		"callers fail":          fixedCallers{err: errors.New("no token on the request")},
		"client without a name": fixedCallers{client: domain.Client{Unrestricted: true}},
		"no callers wired":      nil,
	}
	for name, callers := range tests {
		for tool, args := range toolCalls("acme/payments") {
			t.Run(name+" "+string(tool), func(t *testing.T) {
				t.Parallel()
				tw := newTripwire(t, callers)
				res := call(t, tw.cs, tool, args)
				if !res.IsError || !strings.Contains(textOf(res), string(tool)) {
					t.Fatalf("IsError %v, text %q; want a tool error naming %s", res.IsError, textOf(res), tool)
				}
				if got := tw.reached(); len(got) != 0 {
					t.Fatalf("reached %q with no client", got)
				}
			})
		}
	}
	tw := newTripwire(t, nil)
	if res := call(t, tw.cs, domain.ToolStatus, map[string]any{}); !res.IsError {
		t.Fatalf("forja.status listed with no client: %s", structuredJSON(t, res))
	}
}

// Every run a tool records carries the client that called it.
func TestRuns_recordedUnderTheCallingClient(t *testing.T) {
	t.Parallel()
	asA := func(d *forja.Deps) { d.Callers = fixedCallers{client: restricted(clientA, doctorRepo)} }

	impl := newWiredFixture(t, contractWithWallClock, fixtureWiring{deps: asA})
	implID := decodeImplement(t, callImplement(t, impl.cs, implementIssue)).RunID

	ver := newVerifyFixture(t, verifyWiring{pr: verifyPull(changed("README.md")...), deps: asA})
	verID := decodeVerify(t, callVerify(t, ver.cs, verifyPR)).RunID

	doc := runstore.NewMemory()
	deps := testDeps(doc, &fakeContracts{}, &fakeClaims{calls: &callLog{}})
	deps.Cloner, deps.Gates = &fakeCloner{co: doctorCheckout(doctorContract)}, &fakeGates{report: greenReport()}
	asA(&deps)
	docID := decodeDoctor(t, callDoctor(t, connectDeps(t, deps), doctorRepo)).RunID

	for _, got := range []struct {
		store *runstore.Memory
		id    string
	}{{impl.runs.Memory, implID}, {ver.runs.Memory, verID}, {doc, docID}} {
		run, err := got.store.Get(context.Background(), got.id)
		if err != nil || run.Client != clientA {
			t.Fatalf("run %s = %+v, %v; want it recorded under %s", got.id, run, err, clientA)
		}
	}
}

// A run's client goes back to that client on forja.status, and nowhere
// else: not into the evidence, the pull request, the push, the claim, a
// commit status or a label.
func TestEvidenceAndStatuses_neverNameTheClient(t *testing.T) {
	t.Parallel()
	const secret = "zeta-client-7f3"
	as := func(d *forja.Deps) { d.Callers = fixedCallers{client: restricted(secret, doctorRepo)} }

	impl := newWiredFixture(t, contractWithWallClock, fixtureWiring{deps: as})
	out := decodeImplement(t, callImplement(t, impl.cs, implementIssue))
	ver := newVerifyFixture(t, verifyWiring{pr: verifyPull(changed("README.md")...), deps: as})
	vout := decodeVerify(t, callVerify(t, ver.cs, verifyPR))
	if out.Evidence == "" || vout.Evidence == "" || len(impl.statuses.got()) == 0 || len(ver.statuses.got()) == 0 {
		t.Fatalf("the fixtures rendered no evidence or published no status (%d %d %d %d); the test checks nothing", len(out.Evidence), len(vout.Evidence), len(impl.statuses.got()), len(ver.statuses.got()))
	}

	written := map[string]string{
		"implement evidence": out.Evidence, "verify evidence": vout.Evidence,
		"implement statuses": mustJSON(t, impl.statuses.got()), "verify statuses": mustJSON(t, ver.statuses.got()),
		"pull requests": mustJSON(t, impl.prs.got()), "labels": mustJSON(t, impl.prs.labels.got()),
		"comments": mustJSON(t, impl.prs.comments.got()), "pushes": mustJSON(t, impl.pusher.got()),
		"implement claims": mustJSON(t, impl.claims.requested()), "verify claims": mustJSON(t, ver.claims.requested()),
		"implement output": mustJSON(t, out), "verify output": mustJSON(t, vout),
	}
	for what, body := range written {
		if strings.Contains(body, secret) {
			t.Errorf("%s names the client: %s", what, body)
		}
	}
	if wire := statusWire(t, impl.cs, out.RunID); string(wire["client"]) != `"`+secret+`"` {
		t.Fatalf("forja.status client = %s, want %q", wire["client"], secret)
	}
}

// P1: another client's run id is answered exactly as an id no run has,
// byte for byte, by id and as a cursor; its own client still sees it.
func TestStatus_anotherClientsRunID_answeredByteForByteAsUnknown(t *testing.T) {
	t.Parallel()
	theirs := runningRun("run-of-a")
	theirs.Client = clientA
	shared := storeWith(t, theirs)
	b := connectAs(t, shared, restricted(clientB, doctorRepo))
	bAlone := connectAs(t, runstore.NewMemory(), restricted(clientB, doctorRepo))

	for _, args := range []map[string]any{{"run_id": "run-of-a"}, {"cursor": "run-of-a"}} {
		other := mustJSON(t, callStatus(t, b, args))
		unknown := mustJSON(t, callStatus(t, bAlone, args))
		if other != unknown || !strings.Contains(other, `"isError":true`) {
			t.Fatalf("%v: another client's run = %s\nan unknown id    = %s\nwant the same tool error, byte for byte", args, other, unknown)
		}
		random := mustJSON(t, callStatus(t, b, map[string]any{argName(args): "run-zz-random"}))
		if strings.ReplaceAll(random, "run-zz-random", "run-of-a") != other {
			t.Fatalf("%v: another client's run = %s\na random id      = %s\nwant the same error but for the id", args, other, random)
		}
	}
	a := connectAs(t, shared, restricted(clientA, doctorRepo))
	if wire := statusWire(t, a, "run-of-a"); string(wire["client"]) != `"`+clientA+`"` {
		t.Fatalf("the run's own client got %s, want its run", wire["client"])
	}
}

func argName(args map[string]any) string {
	for k := range args {
		return k
	}
	return ""
}

// interleaved is B's runs in acme/shared with A's between them: same
// starts and higher ids, so a store that did not keep A's out would put
// them on B's pages; and one of A's runs in a repository B does not reach.
func interleaved(withA bool) []domain.Run {
	var runs []domain.Run
	for i := range 5 {
		b := listedRun(fmt.Sprintf("b-%d", i), "acme/shared", domain.ToolImplement, domain.StateSucceeded, time.Duration(i)*time.Hour)
		b.Client = clientB
		runs = append(runs, b)
		if !withA {
			continue
		}
		a := listedRun(fmt.Sprintf("c-%d", i), "acme/shared", domain.ToolImplement, domain.StateFailed, time.Duration(i)*time.Hour)
		a.Client, a.Tokens = clientA, spent
		runs = append(runs, a)
	}
	if withA {
		elsewhere := listedRun("a-elsewhere", "acme/private", domain.ToolDoctor, domain.StateSucceeded, 9*time.Hour)
		elsewhere.Client = clientA
		runs = append(runs, elsewhere)
	}
	return runs
}

// pages walks forja.status from args at limit and returns every page as
// it travelled.
func pages(t *testing.T, cs *sdk.ClientSession, args map[string]any, limit int) []string {
	t.Helper()
	var out []string
	cursor := ""
	for range 20 {
		page := map[string]any{"limit": limit}
		for k, v := range args {
			page[k] = v
		}
		if cursor != "" {
			page["cursor"] = cursor
		}
		res := callStatus(t, cs, page)
		out = append(out, mustJSON(t, res))
		list := callList(t, cs, page)
		if list.NextCursor == "" {
			return out
		}
		cursor = list.NextCursor
	}
	t.Fatal("the walk did not end")
	return nil
}

// P2: B's pages over a repository both clients reach are, byte for byte,
// what they would be had A never run there: no run of A's, the same page
// sizes and cursors, the same per-repository totals.
func TestStatus_listAnotherClientsRuns_neverListedPaginationUnchanged(t *testing.T) {
	t.Parallel()
	b := restricted(clientB, "acme/shared")
	withA := connectAs(t, storeWith(t, interleaved(true)...), b)
	alone := connectAs(t, storeWith(t, interleaved(false)...), b)
	for _, limit := range []int{1, 2, 3, 50} {
		for _, args := range []map[string]any{{}, {"repository": "acme/shared"}} {
			got, want := pages(t, withA, args, limit), pages(t, alone, args, limit)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("limit %d %v: pages with A's runs\n%s\nwant\n%s", limit, args, strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		}
	}
	if ids := summaryIDs(callList(t, withA, map[string]any{})); !reflect.DeepEqual(ids, []string{"b-4", "b-3", "b-2", "b-1", "b-0"}) {
		t.Fatalf("B listed %v, want only its own runs", ids)
	}
	a := connectAs(t, storeWith(t, interleaved(true)...), restricted(clientA, "acme/*"))
	if ids := summaryIDs(callList(t, a, map[string]any{})); len(ids) != 6 || ids[0] != "a-elsewhere" {
		t.Fatalf("A listed %v, want its six runs", ids)
	}
}
