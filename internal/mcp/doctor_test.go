package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/github"
	forja "github.com/fuseone-io/fuseone-forja/internal/mcp"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

const (
	doctorRepo = "fuseone-io/fuseone-forja"
	doctorSHA  = "3d5656a3e223a7bc54b8e8f13086f562b84dc90c"
)

var (
	validContract   = []byte("version: 1\nimage: golang:1.25\ngates:\n  - name: build\n    run: make build\n  - name: test\n    run: make test\n")
	invalidContract = []byte("version: 1\nimage: golang:1.25\ngates:\n  - name: build\n")
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

func callDoctor(t *testing.T, cs *sdk.ClientSession, repo string) *sdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      string(domain.ToolDoctor),
		Arguments: map[string]any{"repository": repo},
	})
	if err != nil {
		t.Fatalf("CallTool(forja.doctor): protocol error %v", err)
	}
	return res
}

func wireOf(t *testing.T, res *sdk.CallToolResult) map[string]json.RawMessage {
	t.Helper()
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(structuredJSON(t, res), &wire); err != nil {
		t.Fatalf("decode wire object: %v", err)
	}
	return wire
}

// decodeDoctor fails the test on a tool error and returns the typed output.
func decodeDoctor(t *testing.T, res *sdk.CallToolResult) forja.DoctorOutput {
	t.Helper()
	if res.IsError {
		t.Fatalf("IsError = true, content: %s", textOf(res))
	}
	var got forja.DoctorOutput
	if err := json.Unmarshal(structuredJSON(t, res), &got); err != nil {
		t.Fatalf("decode DoctorOutput: %v", err)
	}
	return got
}

func TestDoctor_validContract_okWithShaAndNormalisedContract(t *testing.T) {
	t.Parallel()
	contracts := &fakeContracts{src: domain.ContractSource{SHA: doctorSHA, Body: validContract}}
	cs := connectWith(t, runstore.NewMemory(), contracts)

	res := callDoctor(t, cs, doctorRepo)
	got := decodeDoctor(t, res)
	if !got.OK || got.SHA != doctorSHA || got.Repository != doctorRepo {
		t.Fatalf("DoctorOutput = %+v, want ok for %s at %s", got, doctorRepo, doctorSHA)
	}
	if got.Contract == nil || len(got.Contract.Gates) != 2 || got.Contract.Gates[0].Name != "build" {
		t.Fatalf("Contract = %+v, want the two gates starting with build", got.Contract)
	}
	if raw := string(wireOf(t, res)["errors"]); raw != "[]" {
		t.Fatalf("errors on the wire = %s, want an empty array", raw)
	}
	if contracts.repo != doctorRepo {
		t.Fatalf("reader asked for %q, want %q", contracts.repo, doctorRepo)
	}
}

func TestDoctor_invalidContract_okFalseWithPathLineMessage(t *testing.T) {
	t.Parallel()
	contracts := &fakeContracts{src: domain.ContractSource{SHA: doctorSHA, Body: invalidContract}}
	cs := connectWith(t, runstore.NewMemory(), contracts)

	res := callDoctor(t, cs, doctorRepo)
	got := decodeDoctor(t, res)
	if got.OK || got.SHA != doctorSHA {
		t.Fatalf("DoctorOutput = %+v, want ok=false with the sha still set", got)
	}
	want := forja.DoctorError{Path: "gates[0].run", Line: 4, Message: "is required"}
	if len(got.Errors) != 1 || got.Errors[0] != want {
		t.Fatalf("Errors = %+v, want [%+v]", got.Errors, want)
	}
	if _, present := wireOf(t, res)["contract"]; present {
		t.Fatalf("contract present on the wire for a rejected contract: %s", structuredJSON(t, res))
	}
}

func TestDoctor_contractMissing_toolErrorNotCrash(t *testing.T) {
	t.Parallel()
	missing := fmt.Errorf("github: %s: %w at %s", doctorRepo, github.ErrContractMissing, doctorSHA)
	cs := connectWith(t, runstore.NewMemory(), &fakeContracts{err: missing})

	res := callDoctor(t, cs, doctorRepo)
	if !res.IsError {
		t.Fatalf("IsError = false for a repository without a contract: %s", textOf(res))
	}
	text := textOf(res)
	for _, want := range []string{"contract missing", doctorRepo} {
		if !strings.Contains(text, want) {
			t.Errorf("error text %q does not contain %q", text, want)
		}
	}
}

func TestDoctor_readerFails_toolErrorNamesTheTool(t *testing.T) {
	t.Parallel()
	cs := connectWith(t, runstore.NewMemory(), &fakeContracts{err: errors.New("no GitHub credentials")})

	res := callDoctor(t, cs, doctorRepo)
	if !res.IsError {
		t.Fatalf("IsError = false when the reader fails: %s", textOf(res))
	}
	text := textOf(res)
	for _, want := range []string{string(domain.ToolDoctor), "no GitHub credentials"} {
		if !strings.Contains(text, want) {
			t.Errorf("error text %q does not contain %q", text, want)
		}
	}
}

func TestDoctor_wireKeys_snakeCase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body []byte
		keys []string
	}{
		{name: "accepted", body: validContract, keys: []string{"repository", "sha", "ok", "errors", "contract"}},
		{name: "rejected", body: invalidContract, keys: []string{"repository", "sha", "ok", "errors"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			contracts := &fakeContracts{src: domain.ContractSource{SHA: doctorSHA, Body: tt.body}}
			cs := connectWith(t, runstore.NewMemory(), contracts)

			res := callDoctor(t, cs, doctorRepo)
			if res.IsError {
				t.Fatalf("IsError = true, content: %s", textOf(res))
			}
			var got []string
			for key := range wireOf(t, res) {
				got = append(got, key)
			}
			sort.Strings(got)
			sort.Strings(tt.keys)
			if !reflect.DeepEqual(got, tt.keys) {
				t.Fatalf("wire keys = %q, want %q", got, tt.keys)
			}
		})
	}
}

func TestDoctor_contractOnTheWire_snakeCaseFields(t *testing.T) {
	t.Parallel()
	contracts := &fakeContracts{src: domain.ContractSource{SHA: doctorSHA, Body: validContract}}
	cs := connectWith(t, runstore.NewMemory(), contracts)

	res := callDoctor(t, cs, doctorRepo)
	var wire struct {
		Contract map[string]json.RawMessage `json:"contract"`
	}
	if err := json.Unmarshal(structuredJSON(t, res), &wire); err != nil {
		t.Fatalf("decode wire object: %v", err)
	}
	for _, key := range []string{"version", "image", "setup", "services", "registries", "gates", "verify", "proof_paths", "human_merge_paths", "limits", "agent_context"} {
		if _, ok := wire.Contract[key]; !ok {
			t.Errorf("contract.%s absent on the wire: %s", key, wire.Contract)
		}
	}
	var gates []map[string]json.RawMessage
	if err := json.Unmarshal(wire.Contract["gates"], &gates); err != nil || len(gates) == 0 {
		t.Fatalf("contract.gates = %s (%v)", wire.Contract["gates"], err)
	}
	if string(gates[0]["name"]) != `"build"` {
		t.Fatalf("contract.gates[0].name = %s, want \"build\"", gates[0]["name"])
	}
}
