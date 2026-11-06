package mcp_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	forja "github.com/fuseone-io/fuseone-forja/internal/mcp"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

const (
	holderRun     = "run-holder"
	supersededRun = "run-expired"
)

// heldUntil is the holder's lease expiry as the refusal prints it.
var heldUntil = startedAt().Add(45 * time.Minute)

// readFailsFor is a run store whose GetOwned of id fails as a store that
// went away does; every other call is the store's own.
type readFailsFor struct {
	forja.RunStore
	id string
}

func (r readFailsFor) GetOwned(ctx context.Context, client, id string) (domain.Run, error) {
	if id == r.id {
		return domain.Run{}, errors.New("run store down")
	}
	return r.RunStore.GetOwned(ctx, client, id)
}

// holderState is who the store says owns the run that holds the claim.
type holderState struct {
	name string
	// owner is the client the holder run is recorded under; empty records
	// no run.
	owner string
	// readFails makes the store fail the ownership lookup of the holder.
	readFails bool
	own       bool
}

var (
	ownHolder     = holderState{name: "the caller's own run", owner: testClient, own: true}
	foreignHolder = holderState{name: "another client's run", owner: clientB}
	unknownHolder = holderState{name: "a run the store does not know"}
	failedHolder  = holderState{name: "a store that cannot say", owner: testClient, readFails: true}
	holderStates  = []holderState{ownHolder, foreignHolder, unknownHolder, failedHolder}
)

// seeded is the wiring that records the run id under owner, unless owner
// is empty, and makes the store fail the ownership lookup of id when
// fails is set.
func seeded(t *testing.T, id, owner string, fails bool) func(*forja.Deps) {
	t.Helper()
	return func(d *forja.Deps) {
		if owner != "" {
			run := terminalRun(id)
			run.Client = owner
			if err := d.Runs.Put(context.Background(), run); err != nil {
				t.Fatalf("seed %s: %v", id, err)
			}
		}
		if fails {
			d.Runs = readFailsFor{RunStore: d.Runs, id: id}
		}
	}
}

// heldCall wires a server whose claim on the issue is held by holderRun,
// recorded as h says, and calls tool on it.
func heldCall(t *testing.T, tool domain.Tool, h holderState) *sdk.CallToolResult {
	t.Helper()
	holder := domain.Claim{RunID: holderRun, Repository: doctorRepo, Issue: implementIssue, SHA: "sha-holder", CreatedAt: startedAt(), ExpiresAt: heldUntil}
	claims := func(f *fakeClaims) { f.claimErr = &domain.ClaimHeldError{Holder: holder} }
	deps := seeded(t, holderRun, h.owner, h.readFails)
	if tool == domain.ToolVerify {
		return callVerify(t, newVerifyFixture(t, verifyWiring{pr: markdownPull(), claims: claims, deps: deps}).cs, verifyPR)
	}
	fx := newWiredFixture(t, contractWithWallClock, fixtureWiring{claims: claims, deps: deps})
	return callImplement(t, fx.cs, implementIssue)
}

// A claim refused because it is held names the repository, the issue and
// the lease expiry to every caller, and the holder's run only to the
// client that owns it: another client's run, one the store does not know
// and one the store cannot vouch for are named by no id and no client.
func TestClaimHeld_holderOwnership_runIDNamedOnlyToItsOwner(t *testing.T) {
	t.Parallel()
	for _, tool := range []domain.Tool{domain.ToolImplement, domain.ToolVerify} {
		for _, h := range holderStates {
			t.Run(string(tool)+"/"+h.name, func(t *testing.T) {
				t.Parallel()
				res := heldCall(t, tool, h)
				text := textOf(res)
				if !res.IsError {
					t.Fatalf("IsError = false for a held issue: %s", text)
				}
				for _, want := range []string{string(tool), doctorRepo, "issue " + strconv.Itoa(implementIssue), heldUntil.Format(time.RFC3339)} {
					if !strings.Contains(text, want) {
						t.Errorf("error %q does not name %q", text, want)
					}
				}
				if named := strings.Contains(text, holderRun); named != h.own {
					t.Fatalf("error %q names the holder %v, want %v", text, named, h.own)
				}
				if strings.Contains(text, clientB) || strings.Contains(text, "store down") {
					t.Fatalf("error %q names the holder's client or the store's failure", text)
				}
			})
		}
	}
}

// Another client's holder and one no run has are the same refusal, byte
// for byte: the answer tells a caller nothing about whose run it is.
func TestClaimHeld_foreignAndUnknownHolder_answeredAlike(t *testing.T) {
	t.Parallel()
	for _, tool := range []domain.Tool{domain.ToolImplement, domain.ToolVerify} {
		foreign := textOf(heldCall(t, tool, foreignHolder))
		unknown := textOf(heldCall(t, tool, unknownHolder))
		failing := textOf(heldCall(t, tool, failedHolder))
		if foreign != unknown || failing != unknown {
			t.Fatalf("%s: another client's holder = %q\nan unknown holder     = %q\na store that failed   = %q\nwant one answer", tool, foreign, unknown, failing)
		}
	}
}

// supersedingStatus is forja.status, as testClient, of the caller's run
// that took over the expired claim of supersededRun, recorded as h says.
func supersedingStatus(t *testing.T, h holderState) *sdk.CallToolResult {
	t.Helper()
	store := runstore.NewMemory()
	run := terminalRun("run-new")
	run.Supersedes = supersededRun
	mustPut(t, store, run)
	deps := testDeps(store, &fakeContracts{err: errors.New("no contract reader in this test")}, &fakeClaims{calls: &callLog{}, claimErr: errors.New("no claimer in this test")})
	seeded(t, supersededRun, h.owner, h.readFails)(&deps)
	return callStatus(t, connectDeps(t, deps), map[string]any{"run_id": "run-new"})
}

// A run of the caller that took over another client's expired claim is
// reported, once it ran, without supersedes; the record keeps it for the
// operator.
func TestImplement_tookOverAnotherClientsClaim_statusWithoutSupersedes(t *testing.T) {
	t.Parallel()
	fx := newWiredFixture(t, contractWithWallClock, fixtureWiring{
		claims: func(f *fakeClaims) {
			f.supersedes = &domain.ClaimSummary{RunID: supersededRun, SHA: "sha-expired", ExpiresAt: startedAt().Add(-time.Minute)}
		},
		deps: seeded(t, supersededRun, clientB, false),
	})

	out := decodeImplement(t, callImplement(t, fx.cs, implementIssue))
	res := callStatus(t, fx.cs, map[string]any{"run_id": out.RunID})
	if res.IsError || strings.Contains(string(structuredJSON(t, res)), supersededRun) {
		t.Fatalf("status of the run that took over %s = %s, want it without that run", supersededRun, structuredJSON(t, res))
	}
	if run, err := fx.runs.Get(context.Background(), out.RunID); err != nil || run.Supersedes != supersededRun {
		t.Fatalf("recorded run = %+v, %v; want supersedes %s kept for the operator", run, err, supersededRun)
	}
}

// supersedes names the run whose claim the caller's run took over only
// when that run is the caller's too; otherwise it is left out, as if the
// issue had been free.
func TestStatus_supersedes_namedOnlyWhenTheCallersOwn(t *testing.T) {
	t.Parallel()
	for _, h := range holderStates {
		t.Run(h.name, func(t *testing.T) {
			t.Parallel()
			res := supersedingStatus(t, h)
			if res.IsError {
				t.Fatalf("status IsError = true: %s", textOf(res))
			}
			wire := wireOf(t, res)
			raw, present := wire["supersedes"]
			if present != h.own || (h.own && string(raw) != `"`+supersededRun+`"`) {
				t.Fatalf("supersedes = %s (present %v), want present %v: %s", raw, present, h.own, structuredJSON(t, res))
			}
			if !h.own && strings.Contains(string(structuredJSON(t, res)), supersededRun) {
				t.Fatalf("the superseded run of another client is on the wire: %s", structuredJSON(t, res))
			}
		})
	}
}
