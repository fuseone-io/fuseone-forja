package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	forja "github.com/fuseone-io/fuseone-forja/internal/mcp"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

const (
	doctorRepo = "fuseone-io/fuseone-forja"
	doctorSHA  = "3d5656a3e223a7bc54b8e8f13086f562b84dc90c"
	doctorDir  = "/tmp/forja-plan-doctor"
)

var (
	validContract   = []byte("version: 1\nimage: golang:1.25\ngates:\n  - name: build\n    run: make build\n  - name: test\n    run: make test\n")
	invalidContract = []byte("version: 1\nimage: golang:1.25\ngates:\n  - name: build\n")
	// doctorContract is validContract plus one setup step.
	doctorContract = []byte("version: 1\nimage: golang:1.25\nsetup:\n  - go mod download\ngates:\n  - name: build\n    run: make build\n  - name: test\n    run: make test\n")
	// needsContract declares a service and a gate that needs it.
	needsContract = []byte("version: 1\nimage: golang:1.25\nservices:\n  postgres: { image: postgres:17 }\ngates:\n  - name: build\n    run: make build\n  - name: integration\n    run: make integration-test\n    needs: [postgres]\n")
)

// doctorCheckout is a checkout of the default branch holding body as the
// contract.
func doctorCheckout(body []byte) *fakeCheckout {
	return &fakeCheckout{
		sha:    doctorSHA,
		branch: "main",
		dir:    doctorDir,
		files:  map[string]string{".forja.yaml": string(body), "CLAUDE.md": "# Forja\n"},
		dirs:   map[string]bool{},
	}
}

func connectDoctor(t *testing.T, cloner *fakeCloner, gates *fakeGates) *sdk.ClientSession {
	t.Helper()
	return connectWith(t, runstore.NewMemory(), cloner, gates)
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

func TestDoctor_contractGreen_okTrueWithSetupAndGateReports(t *testing.T) {
	t.Parallel()
	co := doctorCheckout(doctorContract)
	cloner := &fakeCloner{co: co}
	gates := &fakeGates{report: greenReport()}
	cs := connectDoctor(t, cloner, gates)

	res := callDoctor(t, cs, doctorRepo)
	got := decodeDoctor(t, res)
	assertGreenHeader(t, got)
	assertGreenSteps(t, got)
	assertRunnerGiven(t, gates, "golang:1.25", doctorDir)
	if cloner.repo != doctorRepo || cloner.branch != "" {
		t.Errorf("cloner asked for %q at %q, want %q at the default branch", cloner.repo, cloner.branch, doctorRepo)
	}
	if co.closed != 1 {
		t.Errorf("Close called %d times, want exactly once", co.closed)
	}
}

// assertGreenHeader checks the identity part of a green doctor result:
// shas, verdict, contract, and nothing to warn about.
func assertGreenHeader(t *testing.T, got forja.DoctorOutput) {
	t.Helper()
	type identity struct {
		repo, sha, contractSHA string
		ok                     bool
	}
	wantIdentity := identity{doctorRepo, doctorSHA, gitBlobSHA(string(doctorContract)), true}
	gotIdentity := identity{got.Repository, got.SHA, got.ContractSHA, got.OK}
	if gotIdentity != wantIdentity {
		t.Fatalf("DoctorOutput identity = %+v, want %+v", gotIdentity, wantIdentity)
	}
	if got.Contract == nil || len(got.Contract.Gates) != 2 || got.Contract.Gates[0].Name != "build" {
		t.Fatalf("Contract = %+v, want the two gates starting with build", got.Contract)
	}
	if got.FailedStep != "" || len(got.Warnings)+len(got.Errors) != 0 {
		t.Errorf("failed_step %q, warnings %q, errors %+v; want none", got.FailedStep, got.Warnings, got.Errors)
	}
}

// assertGreenSteps checks every step of greenReport reached the wire with
// its name, command, exit code, duration and both streams.
func assertGreenSteps(t *testing.T, got forja.DoctorOutput) {
	t.Helper()
	wantSetup := []forja.StepReport{{Name: "setup[0]", Command: "go mod download", Status: "passed", ExitCode: 0, Duration: 3 * time.Second, Stdout: "downloaded\n"}}
	wantGates := []forja.StepReport{
		{Name: "build", Command: "make build", Status: "passed", ExitCode: 0, Duration: 500 * time.Millisecond, Stdout: "built\n"},
		{Name: "test", Command: "make test", Status: "passed", ExitCode: 0, Duration: 2 * time.Second, Stdout: "ok\n", Stderr: "warn\n"},
	}
	if !reflect.DeepEqual(got.Setup, wantSetup) || !reflect.DeepEqual(got.Gates, wantGates) {
		t.Fatalf("setup = %+v\ngates = %+v\nwant %+v\n%+v", got.Setup, got.Gates, wantSetup, wantGates)
	}
}

func assertRunnerGiven(t *testing.T, gates *fakeGates, image, tree string) {
	t.Helper()
	if gates.calls != 1 {
		t.Fatalf("runner called %d times, want once", gates.calls)
	}
	if gates.contract.Image != image || gates.tree != tree {
		t.Errorf("runner given image %q on tree %q, want %q on %q", gates.contract.Image, gates.tree, image, tree)
	}
}

func TestDoctor_gateRed_okFalseWithFailedStep(t *testing.T) {
	t.Parallel()
	cs := connectDoctor(t, &fakeCloner{co: doctorCheckout(doctorContract)}, &fakeGates{report: redReport()})

	res := callDoctor(t, cs, doctorRepo)
	got := decodeDoctor(t, res)
	if got.OK || got.FailedStep != "build" {
		t.Fatalf("ok = %v, failed_step = %q; want false and build", got.OK, got.FailedStep)
	}
	if got.Contract == nil || len(got.Errors) != 0 {
		t.Errorf("a red gate is not a contract defect: contract %v, errors %+v", got.Contract, got.Errors)
	}
	assertRedGates(t, got.Gates)
	if raw := string(wireOf(t, res)["failed_step"]); raw != `"build"` {
		t.Errorf("failed_step on the wire = %s, want \"build\"", raw)
	}
}

// assertRedGates checks the two gates of redReport: the culprit with its
// exit code and stderr, the one after it skipped without an exit code.
func assertRedGates(t *testing.T, gates []forja.StepReport) {
	t.Helper()
	if len(gates) != 2 {
		t.Fatalf("gates = %+v, want both declared gates", gates)
	}
	build, test := gates[0], gates[1]
	if build.Status != "failed" || build.ExitCode != 2 || build.Stderr != "undefined: x\n" {
		t.Errorf("build = %+v, want failed with exit 2 and its stderr", build)
	}
	if test.Status != "skipped" || test.ExitCode != -1 {
		t.Errorf("test = %+v, want skipped with exit -1", test)
	}
}

func TestDoctor_invalidContract_noGateRuns(t *testing.T) {
	t.Parallel()
	gates := &fakeGates{report: greenReport()}
	cs := connectDoctor(t, &fakeCloner{co: doctorCheckout(invalidContract)}, gates)

	res := callDoctor(t, cs, doctorRepo)
	got := decodeDoctor(t, res)
	if got.OK || got.SHA != doctorSHA {
		t.Fatalf("DoctorOutput = %+v, want ok=false with the sha still set", got)
	}
	want := forja.DoctorError{Path: "gates[0].run", Line: 4, Message: "is required"}
	if len(got.Errors) != 1 || got.Errors[0] != want {
		t.Fatalf("Errors = %+v, want [%+v]", got.Errors, want)
	}
	if gates.calls != 0 {
		t.Fatalf("runner called %d times for a rejected contract, want never", gates.calls)
	}
	wire := wireOf(t, res)
	if _, present := wire["contract"]; present {
		t.Errorf("contract present on the wire for a rejected contract: %s", structuredJSON(t, res))
	}
	for _, key := range []string{"setup", "gates", "warnings"} {
		if raw := string(wire[key]); raw != "[]" {
			t.Errorf("%s on the wire = %s, want an empty array", key, raw)
		}
	}
	if got.ContractSHA != gitBlobSHA(string(invalidContract)) {
		t.Errorf("contract_sha = %q, want the rejected contract's blob sha", got.ContractSHA)
	}
}

func TestDoctor_contractMissing_toolErrorNotCrash(t *testing.T) {
	t.Parallel()
	co := doctorCheckout(validContract)
	delete(co.files, ".forja.yaml")
	gates := &fakeGates{report: greenReport()}
	cs := connectDoctor(t, &fakeCloner{co: co}, gates)

	res := callDoctor(t, cs, doctorRepo)
	if !res.IsError {
		t.Fatalf("IsError = false for a repository without a contract: %s", textOf(res))
	}
	text := textOf(res)
	for _, want := range []string{"contract missing", doctorRepo, doctorSHA} {
		if !strings.Contains(text, want) {
			t.Errorf("error text %q does not contain %q", text, want)
		}
	}
	if gates.calls != 0 || co.closed != 1 {
		t.Errorf("runner called %d times, Close called %d times; want 0 and 1", gates.calls, co.closed)
	}
}

func TestDoctor_readerFails_toolErrorNamesTheTool(t *testing.T) {
	t.Parallel()
	cs := connectDoctor(t, &fakeCloner{err: errors.New("no GitHub credentials")}, &fakeGates{})

	res := callDoctor(t, cs, doctorRepo)
	if !res.IsError {
		t.Fatalf("IsError = false when the clone fails: %s", textOf(res))
	}
	text := textOf(res)
	for _, want := range []string{string(domain.ToolDoctor), "no GitHub credentials"} {
		if !strings.Contains(text, want) {
			t.Errorf("error text %q does not contain %q", text, want)
		}
	}
}

func TestDoctor_runnerFails_toolErrorAndCheckoutClosed(t *testing.T) {
	t.Parallel()
	co := doctorCheckout(validContract)
	cs := connectDoctor(t, &fakeCloner{co: co}, &fakeGates{err: errors.New("docker: pull access denied")})

	res := callDoctor(t, cs, doctorRepo)
	if !res.IsError || !strings.Contains(textOf(res), "pull access denied") {
		t.Fatalf("IsError = %v, content %q; want a tool error naming the runner failure", res.IsError, textOf(res))
	}
	if co.closed != 1 {
		t.Errorf("Close called %d times, want exactly once", co.closed)
	}
}

func TestDoctor_gateWithNeeds_warningNamesGateAndServices(t *testing.T) {
	t.Parallel()
	report := domain.GateReport{
		Setup: []domain.GateResult{},
		Gates: []domain.GateResult{passed("build", "make build", "", "", time.Second), skippedStep("integration", "make integration-test")},
	}
	cs := connectDoctor(t, &fakeCloner{co: doctorCheckout(needsContract)}, &fakeGates{report: report})

	got := decodeDoctor(t, callDoctor(t, cs, doctorRepo))
	if !got.OK {
		t.Fatalf("ok = false, want true: a gate waiting for its services does not decide")
	}
	if len(got.Warnings) != 1 {
		t.Fatalf("warnings = %q, want exactly one", got.Warnings)
	}
	for _, want := range []string{"integration", "postgres", "skipped"} {
		if !strings.Contains(got.Warnings[0], want) {
			t.Errorf("warning %q does not say %q", got.Warnings[0], want)
		}
	}
	if got.Gates[1].Status != "skipped" {
		t.Errorf("integration status = %q, want skipped", got.Gates[1].Status)
	}
}

func TestDoctor_wallClockZero_runnerGetsDefaultDeadline(t *testing.T) {
	t.Parallel()
	gates := &fakeGates{report: greenReport()}
	cs := connectDoctor(t, &fakeCloner{co: doctorCheckout(doctorContract)}, gates)

	before := time.Now()
	decodeDoctor(t, callDoctor(t, cs, doctorRepo))
	assertDeadlineWithin(t, gates, before, 30*time.Minute)
}

func TestDoctor_wallClockSet_runnerGetsThatDeadline(t *testing.T) {
	t.Parallel()
	body := append(append([]byte{}, doctorContract...), []byte("limits:\n  wall_clock: 7m\n")...)
	gates := &fakeGates{report: greenReport()}
	cs := connectDoctor(t, &fakeCloner{co: doctorCheckout(body)}, gates)

	before := time.Now()
	decodeDoctor(t, callDoctor(t, cs, doctorRepo))
	assertDeadlineWithin(t, gates, before, 7*time.Minute)
}

// assertDeadlineWithin checks the runner's deadline is before + wallClock,
// give or take the time the call itself took.
func assertDeadlineWithin(t *testing.T, gates *fakeGates, before time.Time, wallClock time.Duration) {
	t.Helper()
	if !gates.hasDL {
		t.Fatal("runner context has no deadline")
	}
	low, high := before.Add(wallClock), time.Now().Add(wallClock)
	if gates.deadline.Before(low) || gates.deadline.After(high) {
		t.Fatalf("deadline = %v, want within [%v, %v]", gates.deadline, low, high)
	}
}

func TestDoctor_logOver64KiB_tailKeptAndTruncatedTrue(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("x", 70*1024) + "TAIL"
	report := greenReport()
	report.Gates[0].Stdout = []byte(big)
	report.Gates[0].Stderr = []byte("short")
	cs := connectDoctor(t, &fakeCloner{co: doctorCheckout(doctorContract)}, &fakeGates{report: report})

	got := decodeDoctor(t, callDoctor(t, cs, doctorRepo))
	build := got.Gates[0]
	if len(build.Stdout) != 64*1024 || !strings.HasSuffix(build.Stdout, "TAIL") || !build.Truncated {
		t.Fatalf("stdout len %d, suffix %q, truncated %v; want 64 KiB ending in TAIL and truncated", len(build.Stdout), build.Stdout[len(build.Stdout)-4:], build.Truncated)
	}
	if build.Stderr != "short" {
		t.Errorf("stderr = %q, want untouched", build.Stderr)
	}
	if got.Gates[1].Truncated {
		t.Errorf("a short log is marked truncated")
	}
}

func TestDoctor_wireKeys_snakeCase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body []byte
		keys []string
	}{
		{name: "accepted", body: validContract, keys: []string{"repository", "sha", "contract_sha", "ok", "errors", "contract", "setup", "gates", "warnings"}},
		{name: "rejected", body: invalidContract, keys: []string{"repository", "sha", "contract_sha", "ok", "errors", "setup", "gates", "warnings"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cs := connectDoctor(t, &fakeCloner{co: doctorCheckout(tt.body)}, &fakeGates{report: greenReport()})

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

func TestDoctor_stepOnTheWire_snakeCaseFields(t *testing.T) {
	t.Parallel()
	cs := connectDoctor(t, &fakeCloner{co: doctorCheckout(doctorContract)}, &fakeGates{report: greenReport()})

	res := callDoctor(t, cs, doctorRepo)
	var steps []map[string]json.RawMessage
	if err := json.Unmarshal(wireOf(t, res)["gates"], &steps); err != nil || len(steps) != 2 {
		t.Fatalf("gates = %s (%v)", wireOf(t, res)["gates"], err)
	}
	want := []string{"name", "command", "status", "exit_code", "duration", "stdout", "stderr", "truncated"}
	var got []string
	for key := range steps[0] {
		got = append(got, key)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("gates[0] keys = %q, want %q", got, want)
	}
	if string(steps[0]["duration"]) != "500000000" {
		t.Errorf("gates[0].duration = %s, want nanoseconds 500000000", steps[0]["duration"])
	}
}

func TestDoctor_contractOnTheWire_snakeCaseFields(t *testing.T) {
	t.Parallel()
	cs := connectDoctor(t, &fakeCloner{co: doctorCheckout(validContract)}, &fakeGates{report: greenReport()})

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
