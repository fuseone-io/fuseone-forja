package gates_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/gates"
)

const (
	pilotImage = "golang:1.25"
	testRunID  = "run-gates-1"
)

// pilotContract is two setup steps and three gates, like the pilot's.
func pilotContract() domain.Contract {
	return domain.Contract{
		Image: pilotImage,
		Setup: []string{"go mod download", "go install lint@v2"},
		Gates: []domain.Gate{
			{Name: "build", Run: "make build"},
			{Name: "vet", Run: "make vet"},
			{Name: "test", Run: "make test"},
		},
	}
}

// allGreen scripts every step of pilotContract to exit 0 with a stream of
// its own.
func allGreen() map[string]scripted {
	return map[string]scripted{
		"go mod download":    {stdout: "downloaded\n"},
		"go install lint@v2": {},
		"make build":         {stdout: "built\n"},
		"make vet":           {stderr: "vet notes\n"},
		"make test":          {stdout: "ok\n"},
	}
}

// ticking is a clock that advances one second per reading, so every
// measured duration is a known, non-zero number.
func ticking() func() time.Time {
	t := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	return func() time.Time {
		t = t.Add(time.Second)
		return t
	}
}

func newRunner(ws *fakeWorkspace) (*gates.Runner, *fakeLauncher) {
	launcher := &fakeLauncher{ws: ws}
	return &gates.Runner{Launcher: launcher, Now: ticking()}, launcher
}

func names(results []domain.GateResult) []string {
	out := make([]string, 0, len(results))
	for _, r := range results {
		out = append(out, r.Name)
	}
	return out
}

func statuses(results []domain.GateResult) []domain.StepStatus {
	out := make([]domain.StepStatus, 0, len(results))
	for _, r := range results {
		out = append(out, r.Status)
	}
	return out
}

func TestRunner_setupThenGates_runInDeclaredOrder(t *testing.T) {
	t.Parallel()
	ws := &fakeWorkspace{script: allGreen()}
	runner, launcher := newRunner(ws)

	report, err := runner.Run(context.Background(), testRunID, pilotContract(), "/tmp/forja-plan-1")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	wantCommands := []string{"go mod download", "go install lint@v2", "make build", "make vet", "make test"}
	if got := ws.ran(); !reflect.DeepEqual(got, wantCommands) {
		t.Fatalf("commands ran = %q, want %q", got, wantCommands)
	}
	if launcher.runID != testRunID || launcher.image != pilotImage || launcher.tree != "/tmp/forja-plan-1" {
		t.Fatalf("launched run %q from %q on %q, want %q from %q on /tmp/forja-plan-1", launcher.runID, launcher.image, launcher.tree, testRunID, pilotImage)
	}
	assertGreenReport(t, report)
	assertResult(t, report.Setup[0], "go mod download", domain.StepPassed, 0, "downloaded\n", "")
	assertResult(t, report.Gates[1], "make vet", domain.StepPassed, 0, "", "vet notes\n")
}

// The registries the workspace gets a proxy for are the contract's, which
// the caller read from the base commit; none declared is none proxied.
func TestRunner_contractRegistries_reachTheLauncher(t *testing.T) {
	t.Parallel()
	for _, registries := range [][]string{{"ghcr.io/my-org", "npm.pkg.github.com"}, {}} {
		runner, launcher := newRunner(&fakeWorkspace{script: allGreen()})
		c := pilotContract()
		c.Registries = registries

		if _, err := runner.Run(context.Background(), testRunID, c, "/tree"); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if !reflect.DeepEqual(launcher.registries, registries) {
			t.Errorf("launched with registries %q, want the contract's %q", launcher.registries, registries)
		}
	}
}

// assertGreenReport checks the names, order, verdict and measured
// durations of a run of pilotContract where everything passed.
func assertGreenReport(t *testing.T, report domain.GateReport) {
	t.Helper()
	if got := names(report.Setup); !reflect.DeepEqual(got, []string{"setup[0]", "setup[1]"}) {
		t.Errorf("setup names = %q, want setup[0], setup[1]", got)
	}
	if got := names(report.Gates); !reflect.DeepEqual(got, []string{"build", "vet", "test"}) {
		t.Errorf("gate names = %q, want the contract's order", got)
	}
	if !report.OK() || report.FailedStep != "" {
		t.Errorf("report not OK: FailedStep = %q", report.FailedStep)
	}
	for _, r := range append(report.Setup, report.Gates...) {
		if r.Duration != time.Second {
			t.Errorf("%s: Duration = %v, want the clock's one second between start and end", r.Name, r.Duration)
		}
	}
}

func assertResult(t *testing.T, got domain.GateResult, command string, status domain.StepStatus, exit int, stdout, stderr string) {
	t.Helper()
	if got.Command != command || got.Status != status || got.ExitCode != exit {
		t.Errorf("%s = {%q %s %d}, want {%q %s %d}", got.Name, got.Command, got.Status, got.ExitCode, command, status, exit)
	}
	if string(got.Stdout) != stdout || string(got.Stderr) != stderr {
		t.Errorf("%s streams = (%q, %q), want (%q, %q)", got.Name, got.Stdout, got.Stderr, stdout, stderr)
	}
}

// The workspace bounds the streams; the runner carries its word on it
// into the result, red or green, and never raises it on its own.
func TestRunner_streamCutByWorkspace_truncatedCarriedIntoResult(t *testing.T) {
	t.Parallel()
	script := allGreen()
	script["make build"] = scripted{stdout: "...last lines\n", truncated: true}
	script["make vet"] = scripted{exit: 2, stderr: "...last lines\n", truncated: true}
	ws := &fakeWorkspace{script: script}
	runner, _ := newRunner(ws)

	report, err := runner.Run(context.Background(), testRunID, pilotContract(), "/tmp/forja-plan-1")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, r := range report.Setup {
		if r.Truncated {
			t.Errorf("%s: Truncated = true, the workspace did not cut it", r.Name)
		}
	}
	build, vet := report.Gates[0], report.Gates[1]
	if !build.Truncated || string(build.Stdout) != "...last lines\n" {
		t.Errorf("build = {truncated %v, stdout %q}, want {true, the tail as the workspace gave it}", build.Truncated, build.Stdout)
	}
	if !vet.Truncated || vet.Status != domain.StepFailed {
		t.Errorf("vet = {truncated %v, status %s}, want {true, failed}", vet.Truncated, vet.Status)
	}
}

func TestRunner_firstGateRed_laterGatesSkipped(t *testing.T) {
	t.Parallel()
	script := allGreen()
	script["make vet"] = scripted{exit: 2, stderr: "vet: shadowed err\n"}
	ws := &fakeWorkspace{script: script}
	runner, _ := newRunner(ws)

	report, err := runner.Run(context.Background(), testRunID, pilotContract(), "/tree")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.FailedStep != "vet" || report.OK() {
		t.Fatalf("FailedStep = %q, OK = %v; want vet and not OK", report.FailedStep, report.OK())
	}
	want := []domain.StepStatus{domain.StepPassed, domain.StepFailed, domain.StepSkipped}
	if got := statuses(report.Gates); !reflect.DeepEqual(got, want) {
		t.Fatalf("gate statuses = %v, want %v", got, want)
	}
	assertResult(t, report.Gates[1], "make vet", domain.StepFailed, 2, "", "vet: shadowed err\n")
	assertResult(t, report.Gates[2], "make test", domain.StepSkipped, domain.NoExitCode, "", "")
	if report.Gates[2].Duration != 0 {
		t.Errorf("skipped gate Duration = %v, want 0", report.Gates[2].Duration)
	}
	if ran := ws.ran(); ran[len(ran)-1] != "make vet" {
		t.Errorf("commands ran = %q, want none after make vet", ran)
	}
}

func TestRunner_setupRed_noGateRuns(t *testing.T) {
	t.Parallel()
	script := allGreen()
	script["go install lint@v2"] = scripted{exit: 1, stderr: "requires go >= 1.26.0\n"}
	ws := &fakeWorkspace{script: script}
	runner, _ := newRunner(ws)

	report, err := runner.Run(context.Background(), testRunID, pilotContract(), "/tree")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.FailedStep != "setup[1]" {
		t.Fatalf("FailedStep = %q, want setup[1]", report.FailedStep)
	}
	if got := statuses(report.Setup); !reflect.DeepEqual(got, []domain.StepStatus{domain.StepPassed, domain.StepFailed}) {
		t.Errorf("setup statuses = %v, want passed, failed", got)
	}
	want := []domain.StepStatus{domain.StepSkipped, domain.StepSkipped, domain.StepSkipped}
	if got := statuses(report.Gates); !reflect.DeepEqual(got, want) {
		t.Errorf("gate statuses = %v, want every gate skipped", got)
	}
	if got := ws.ran(); !reflect.DeepEqual(got, []string{"go mod download", "go install lint@v2"}) {
		t.Errorf("commands ran = %q, want the two setup steps only", got)
	}
	if len(report.Gates) != 3 {
		t.Errorf("report lists %d gates, want every declared gate even when none ran", len(report.Gates))
	}
}

func TestRunner_closeCalled_onSuccessFailureAndTimeout(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		script     func() map[string]scripted
		deadline   time.Duration
		failedStep string
		status     domain.StepStatus
	}{
		{name: "success", script: allGreen, status: domain.StepPassed},
		{name: "red gate", script: func() map[string]scripted {
			s := allGreen()
			s["make build"] = scripted{exit: 1}
			return s
		}, failedStep: "build", status: domain.StepFailed},
		{name: "timeout", script: func() map[string]scripted {
			s := allGreen()
			s["make build"] = scripted{block: true, stdout: "compiling"}
			return s
		}, deadline: 50 * time.Millisecond, failedStep: "build", status: domain.StepTimedOut},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ws := &fakeWorkspace{script: tt.script()}
			runner, _ := newRunner(ws)
			ctx := context.Background()
			if tt.deadline > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tt.deadline)
				defer cancel()
			}

			report, err := runner.Run(ctx, testRunID, pilotContract(), "/tree")
			if err != nil {
				t.Fatalf("Run: %v, want nil: the run's own deadline is a result", err)
			}
			if ws.closes() != 1 {
				t.Fatalf("Close called %d times, want exactly once", ws.closes())
			}
			if report.FailedStep != tt.failedStep || report.Gates[0].Status != tt.status {
				t.Fatalf("FailedStep = %q, build status = %s; want %q, %s", report.FailedStep, report.Gates[0].Status, tt.failedStep, tt.status)
			}
			if tt.status == domain.StepTimedOut {
				assertTimedOut(t, report)
			}
		})
	}
}

// assertTimedOut checks the shape of a report whose first gate outlived
// the deadline: no exit code, the partial log kept, the rest skipped.
func assertTimedOut(t *testing.T, report domain.GateReport) {
	t.Helper()
	build := report.Gates[0]
	if build.ExitCode != domain.NoExitCode || string(build.Stdout) != "compiling" {
		t.Errorf("timed out gate = {exit %d, stdout %q}, want {exit -1, stdout \"compiling\"}", build.ExitCode, build.Stdout)
	}
	if build.Duration <= 0 {
		t.Errorf("timed out gate Duration = %v, want the time it ran", build.Duration)
	}
	if got := statuses(report.Gates[1:]); !reflect.DeepEqual(got, []domain.StepStatus{domain.StepSkipped, domain.StepSkipped}) {
		t.Errorf("statuses after the timeout = %v, want skipped", got)
	}
}

func TestRunner_ctxCancelled_isAnErrorNotAReport(t *testing.T) {
	t.Parallel()
	script := allGreen()
	script["make build"] = scripted{block: true}
	ws := &fakeWorkspace{script: script}
	runner, _ := newRunner(ws)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Cancel as soon as the blocked command has been received.
		for len(ws.ran()) < 3 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()

	_, err := runner.Run(ctx, testRunID, pilotContract(), "/tree")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run after cancellation = %v, want context.Canceled", err)
	}
	if ws.closes() != 1 {
		t.Fatalf("Close called %d times, want exactly once", ws.closes())
	}
}

func TestRunner_launchFails_errorNamesImageAndNothingRan(t *testing.T) {
	t.Parallel()
	launcher := &fakeLauncher{err: errors.New("pull access denied")}
	runner := &gates.Runner{Launcher: launcher, Now: ticking()}

	_, err := runner.Run(context.Background(), testRunID, pilotContract(), "/tree")
	if err == nil {
		t.Fatal("Run with a failed launch = nil error")
	}
	for _, want := range []string{pilotImage, "pull access denied"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func TestRunner_execFailsToRun_errorNotAFailedGate(t *testing.T) {
	t.Parallel()
	script := allGreen()
	script["make build"] = scripted{err: errors.New("daemon: connection reset")}
	ws := &fakeWorkspace{script: script}
	runner, _ := newRunner(ws)

	_, err := runner.Run(context.Background(), testRunID, pilotContract(), "/tree")
	if err == nil || !strings.Contains(err.Error(), "connection reset") || !strings.Contains(err.Error(), "build") {
		t.Fatalf("Run = %v, want an error naming the gate and the daemon failure", err)
	}
	if ws.closes() != 1 {
		t.Fatalf("Close called %d times, want exactly once", ws.closes())
	}
}

func TestRunner_closeFails_reportedAsError(t *testing.T) {
	t.Parallel()
	ws := &fakeWorkspace{script: allGreen(), closeErr: errors.New("daemon: remove failed")}
	runner, _ := newRunner(ws)

	report, err := runner.Run(context.Background(), testRunID, pilotContract(), "/tree")
	if err == nil || !strings.Contains(err.Error(), "remove failed") {
		t.Fatalf("Run = %v, want the Close failure: a container left behind is not a clean run", err)
	}
	if !report.OK() {
		t.Errorf("the report of the run itself is lost: %+v", report)
	}
}

func TestRunner_noNow_fails(t *testing.T) {
	t.Parallel()
	runner := &gates.Runner{Launcher: &fakeLauncher{ws: &fakeWorkspace{script: allGreen()}}}

	_, err := runner.Run(context.Background(), testRunID, pilotContract(), "/tree")
	if err == nil {
		t.Fatal("Run without a clock = nil error, want a configuration error")
	}
}

// manualClock reads the same instant until a fake moves it, so the only
// time that passes is what the fakes say their calls took.
type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *manualClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *manualClock) advance(d time.Duration) func() {
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.t = c.t.Add(d)
	}
}

// What each call takes on a timed run's clock.
const (
	launchTakes = 30 * time.Second
	execTakes   = time.Minute
	startTakes  = 10 * time.Second
	closeTakes  = 5 * time.Second
)

// timedRun runs c on ws with every call taking its known time.
func timedRun(t *testing.T, ws *fakeWorkspace, c domain.Contract) (domain.GateReport, error) {
	t.Helper()
	clock := &manualClock{t: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)}
	ws.onExec, ws.onStart, ws.onClose = clock.advance(execTakes), clock.advance(startTakes), clock.advance(closeTakes)
	runner := &gates.Runner{Launcher: &fakeLauncher{ws: ws, onLaunch: clock.advance(launchTakes)}, Now: clock.now}
	return runner.Run(context.Background(), testRunID, c, "/tree")
}

// The workspace counts from before its launch until Close returned, the
// sidecar from before its start until the same Close: 30s, three
// commands, the start, a command and 5s for the workspace, 1m15s for
// postgres.
func TestRunner_serviceStarted_computeCountsWorkspaceAndSidecar(t *testing.T) {
	t.Parallel()
	ws := &fakeWorkspace{script: servicesScript(), services: ready()}

	report, err := timedRun(t, ws, servicesContract(build, integration))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	workspace := launchTakes + 4*execTakes + startTakes + closeTakes
	postgres := startTakes + execTakes + closeTakes
	if report.Compute != workspace+postgres {
		t.Fatalf("Compute = %v, want %v: the workspace's %v and postgres's %v", report.Compute, workspace+postgres, workspace, postgres)
	}
}

// postgres is needed by both gates and started once: it is counted once,
// from its one start; redis from its own.
func TestRunner_serviceNeededTwice_countedOnce(t *testing.T) {
	t.Parallel()
	ws := &fakeWorkspace{script: servicesScript(), services: ready()}
	e2e := domain.Gate{Name: "e2e", Run: "make e2e", Needs: []string{"postgres", "redis"}}

	report, err := timedRun(t, ws, servicesContract(integration, e2e))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	workspace := launchTakes + 4*execTakes + 2*startTakes + closeTakes
	postgres := 2*startTakes + 2*execTakes + closeTakes
	redis := startTakes + execTakes + closeTakes
	if want := workspace + postgres + redis; report.Compute != want {
		t.Fatalf("Compute = %v, want %v: workspace %v, postgres %v once, redis %v", report.Compute, want, workspace, postgres, redis)
	}
}

// A sidecar that never became ready was still started: its container is
// counted from before its start until Close, like a ready one.
func TestRunner_serviceNotReady_sidecarStillCounted(t *testing.T) {
	t.Parallel()
	services := ready()
	services["postgres"] = scriptedService{notReady: true, reason: "service postgres exited with code 1"}
	ws := &fakeWorkspace{script: servicesScript(), services: services}

	report, err := timedRun(t, ws, servicesContract(integration))
	if err != nil || report.FailedStep != "integration" {
		t.Fatalf("Run = failed %q, %v; want integration red", report.FailedStep, err)
	}
	workspace := launchTakes + 2*execTakes + startTakes + closeTakes
	postgres := startTakes + closeTakes
	if report.Compute != workspace+postgres {
		t.Fatalf("Compute = %v, want %v: the workspace's %v and the unready postgres's %v", report.Compute, workspace+postgres, workspace, postgres)
	}
}

// A run that ended in an error has no report but still had its
// containers up: the compute comes back with the error.
func TestRunner_execError_reportCarriesCompute(t *testing.T) {
	t.Parallel()
	script := servicesScript()
	script["make integration"] = scripted{err: errors.New("daemon: connection reset")}
	ws := &fakeWorkspace{script: script, services: ready()}

	report, err := timedRun(t, ws, servicesContract(integration))
	if err == nil {
		t.Fatal("Run = nil error, want the exec failure")
	}
	workspace := launchTakes + 3*execTakes + startTakes + closeTakes
	postgres := startTakes + execTakes + closeTakes
	if report.Compute != workspace+postgres || report.Gates != nil {
		t.Fatalf("report = compute %v gates %+v, want only the compute %v", report.Compute, report.Gates, workspace+postgres)
	}
}

func TestRunner_redGate_computeReported(t *testing.T) {
	t.Parallel()
	script := allGreen()
	script["make vet"] = scripted{exit: 2}
	ws := &fakeWorkspace{script: script}

	report, err := timedRun(t, ws, pilotContract())
	if err != nil || report.FailedStep != "vet" {
		t.Fatalf("Run = failed %q, %v; want vet red", report.FailedStep, err)
	}
	if want := launchTakes + 4*execTakes + closeTakes; report.Compute != want {
		t.Fatalf("Compute = %v, want %v: the skipped gate ran nothing", report.Compute, want)
	}
}

// A launch that failed may have created containers before it gave up:
// the attempt is compute, up to when the launch returned.
func TestRunner_launchFails_computeOfTheAttempt(t *testing.T) {
	t.Parallel()
	clock := &manualClock{}
	runner := &gates.Runner{Launcher: &fakeLauncher{err: errors.New("pull access denied"), onLaunch: clock.advance(launchTakes)}, Now: clock.now}

	report, err := runner.Run(context.Background(), testRunID, pilotContract(), "/tree")
	if err == nil || report.Compute != launchTakes {
		t.Fatalf("Run = compute %v, %v; want the launch failure with the %v it took", report.Compute, err, launchTakes)
	}
}

// twoDenials is a gateway report with more attempts counted than listed,
// so a runner that rebuilt the report from the list would be caught.
func twoDenials() domain.EgressReport {
	at := time.Date(2026, 11, 1, 9, 0, 3, 0, time.UTC)
	return domain.EgressReport{
		Denied: []domain.EgressDenial{
			{At: at, Kind: domain.EgressHTTP, Destination: "example.com:80"},
			{At: at, Kind: domain.EgressConnect, Destination: "example.com:443"},
		},
		Count: 7,
	}
}

func TestRunner_deniedEgress_reportCarriesWorkspaceEgress(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		script func() map[string]scripted
	}{
		{name: "green run", script: allGreen},
		{name: "red gate", script: func() map[string]scripted {
			s := allGreen()
			s["make build"] = scripted{exit: 1, stderr: "wget: server returned error: HTTP/1.1 403 Forbidden\n"}
			return s
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ws := &fakeWorkspace{script: tt.script(), egress: twoDenials()}
			runner, _ := newRunner(ws)

			report, err := runner.Run(context.Background(), testRunID, pilotContract(), "/tree")
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if !reflect.DeepEqual(report.Egress, twoDenials()) {
				t.Fatalf("report.Egress = %+v, want the workspace's %+v", report.Egress, twoDenials())
			}
			if ws.reads() != 1 {
				t.Fatalf("egress read %d times, want once", ws.reads())
			}
		})
	}
}

// A gate that outlives the deadline is exactly the one that may have been
// trying to get out: its denials are read on a context of their own,
// before the gateway is removed.
func TestRunner_stepTimedOut_egressStillCollectedBeforeClose(t *testing.T) {
	t.Parallel()
	script := allGreen()
	script["make build"] = scripted{block: true}
	ws := &fakeWorkspace{script: script, egress: twoDenials()}
	runner, _ := newRunner(ws)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	report, err := runner.Run(ctx, testRunID, pilotContract(), "/tree")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.FailedStep != "build" || !reflect.DeepEqual(report.Egress, twoDenials()) {
		t.Fatalf("report = {failed %q, egress %+v}, want {build, %+v}", report.FailedStep, report.Egress, twoDenials())
	}
	if ws.closes() != 1 {
		t.Fatalf("Close called %d times, want once, after the egress was read", ws.closes())
	}
}

// A report whose denials could not be read is not evidence of a clean
// run: the runner says so, and still removes the workspace.
func TestRunner_egressUnreadable_errorAndWorkspaceClosed(t *testing.T) {
	t.Parallel()
	ws := &fakeWorkspace{script: allGreen(), egressErr: errors.New("gateway log: no such container")}
	runner, _ := newRunner(ws)

	_, err := runner.Run(context.Background(), testRunID, pilotContract(), "/tree")
	if err == nil || !strings.Contains(err.Error(), "no such container") {
		t.Fatalf("Run = %v, want the egress failure", err)
	}
	if ws.closes() != 1 {
		t.Fatalf("Close called %d times, want exactly once", ws.closes())
	}
}

// registryAccesses is a proxy report with more requests counted than
// listed, so a runner that rebuilt it from the list would be caught.
func registryAccesses() domain.RegistryReport {
	at := time.Date(2026, 11, 2, 9, 0, 3, 0, time.UTC)
	return domain.RegistryReport{
		Accesses: []domain.RegistryAccess{
			{At: at, Registry: "ghcr.io/my-org", Path: "/pkg", Outcome: domain.RegistryAllowed, Status: 200},
			{At: at, Registry: "example.com", Path: "/x", Outcome: domain.RegistryDenied, Status: 403},
		},
		Count: 9,
	}
}

// The accesses are read after the last step, timed out or red ones
// included, and before the workspace is removed with its proxy.
func TestRunner_registryReport_carriedAfterSteps(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		script   func() map[string]scripted
		deadline time.Duration
	}{
		{name: "green run", script: allGreen},
		{name: "red gate", script: func() map[string]scripted {
			s := allGreen()
			s["make build"] = scripted{exit: 1}
			return s
		}},
		{name: "timed out gate", script: func() map[string]scripted {
			s := allGreen()
			s["make build"] = scripted{block: true}
			return s
		}, deadline: 50 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ws := &fakeWorkspace{script: tt.script(), registry: registryAccesses()}
			runner, _ := newRunner(ws)
			ctx := context.Background()
			if tt.deadline > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tt.deadline)
				defer cancel()
			}

			report, err := runner.Run(ctx, testRunID, pilotContract(), "/tree")
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if !reflect.DeepEqual(report.Registry, registryAccesses()) {
				t.Fatalf("report.Registry = %+v, want the workspace's %+v", report.Registry, registryAccesses())
			}
			events := ws.log()
			reads, at := ws.registryRead()
			if reads != 1 || at != len(events)-1 || events[at] != "close" {
				t.Fatalf("registry read %d times after %d of events %q, want once, after the last step and before close", reads, at, events)
			}
		})
	}
}

// A run whose accesses could not be read has no evidence of what it
// fetched: the runner says so, and still removes the workspace.
func TestRunner_registryReadFails_runErrors(t *testing.T) {
	t.Parallel()
	ws := &fakeWorkspace{script: allGreen(), registryErr: errors.New("registry proxy log: no such container")}
	runner, _ := newRunner(ws)

	_, err := runner.Run(context.Background(), testRunID, pilotContract(), "/tree")
	if err == nil || !strings.Contains(err.Error(), "no such container") || !strings.Contains(err.Error(), "registry") {
		t.Fatalf("Run = %v, want the registry read failure", err)
	}
	if ws.closes() != 1 {
		t.Fatalf("Close called %d times, want exactly once", ws.closes())
	}
}

// A run that ended in an error has no report to carry the denials in; the
// runner does not read them, and still removes the workspace.
func TestRunner_execFailsToRun_egressNotRead(t *testing.T) {
	t.Parallel()
	script := allGreen()
	script["make build"] = scripted{err: errors.New("daemon: connection reset")}
	ws := &fakeWorkspace{script: script, egress: twoDenials()}
	runner, _ := newRunner(ws)

	if _, err := runner.Run(context.Background(), testRunID, pilotContract(), "/tree"); err == nil {
		t.Fatal("Run = nil error, want the exec failure")
	}
	if ws.reads() != 0 || ws.closes() != 1 {
		t.Fatalf("egress read %d times, Close %d times; want 0 and 1", ws.reads(), ws.closes())
	}
}
