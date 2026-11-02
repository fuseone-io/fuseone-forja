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

var (
	postgres = domain.Service{Image: "postgres:16", Env: map[string]string{"POSTGRES_HOST_AUTH_METHOD": "trust"}}
	redis    = domain.Service{Image: "redis:8-alpine", Env: map[string]string{}}
)

// servicesContract is pilotContract's setup, two declared services and
// the given gates.
func servicesContract(gs ...domain.Gate) domain.Contract {
	c := pilotContract()
	c.Services = map[string]domain.Service{"postgres": postgres, "redis": redis}
	c.Gates = gs
	return c
}

// servicesScript is allGreen plus the commands of the gates that need a
// service.
func servicesScript() map[string]scripted {
	s := allGreen()
	s["make integration"] = scripted{stdout: "integration ok\n"}
	s["make e2e"] = scripted{stdout: "e2e ok\n"}
	return s
}

func ready() map[string]scriptedService {
	return map[string]scriptedService{"postgres": {log: "database system is ready\n"}, "redis": {}}
}

var (
	build       = domain.Gate{Name: "build", Run: "make build"}
	integration = domain.Gate{Name: "integration", Run: "make integration", Needs: []string{"postgres"}}
	test        = domain.Gate{Name: "test", Run: "make test"}
)

var setupEvents = []string{"go mod download", "go install lint@v2"}

func run(t *testing.T, ws *fakeWorkspace, c domain.Contract) domain.GateReport {
	t.Helper()
	runner, _ := newRunner(ws)
	report, err := runner.Run(context.Background(), testRunID, c, "/tree")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return report
}

func TestRunner_gateWithNeeds_serviceStartedBeforeTheGate(t *testing.T) {
	t.Parallel()
	ws := &fakeWorkspace{script: servicesScript(), services: ready()}

	report := run(t, ws, servicesContract(build, integration))
	want := append(append([]string{}, setupEvents...), "make build", "start postgres", "make integration", "close")
	if got := ws.log(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %q, want %q", got, want)
	}
	if !report.OK() {
		t.Fatalf("report not OK: FailedStep = %q", report.FailedStep)
	}
	assertResult(t, report.Gates[1], "make integration", domain.StepPassed, 0, "integration ok\n", "")
	if report.Gates[1].Reason != "" {
		t.Errorf("Reason = %q on a gate whose service was ready", report.Gates[1].Reason)
	}
	if n := ws.stillRunning(); n != 0 {
		t.Errorf("%d services still running after the run, want every one torn down with the workspace", n)
	}
}

func TestRunner_serviceNeededByTwoGates_startedOnce(t *testing.T) {
	t.Parallel()
	ws := &fakeWorkspace{script: servicesScript(), services: ready()}
	e2e := domain.Gate{Name: "e2e", Run: "make e2e", Needs: []string{"postgres", "redis"}}

	report := run(t, ws, servicesContract(integration, e2e))
	want := append(append([]string{}, setupEvents...), "start postgres", "make integration", "start redis", "make e2e", "close")
	if got := ws.log(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %q, want %q", got, want)
	}
	if !report.OK() {
		t.Fatalf("report not OK: FailedStep = %q", report.FailedStep)
	}
}

func TestRunner_gateNeedsTwo_startedInNeedsOrder(t *testing.T) {
	t.Parallel()
	ws := &fakeWorkspace{script: servicesScript(), services: ready()}
	e2e := domain.Gate{Name: "e2e", Run: "make e2e", Needs: []string{"redis", "postgres"}}

	run(t, ws, servicesContract(e2e))
	want := append(append([]string{}, setupEvents...), "start redis", "start postgres", "make e2e", "close")
	if got := ws.log(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

func TestRunner_serviceNoGateNeeds_neverStarted(t *testing.T) {
	t.Parallel()
	ws := &fakeWorkspace{script: servicesScript(), services: ready()}

	run(t, ws, servicesContract(build, integration, test))
	if _, ok := ws.started("redis"); ok {
		t.Fatalf("redis started, but no gate needs it: events %q", ws.log())
	}
	if _, ok := ws.started("postgres"); !ok {
		t.Fatalf("postgres never started, but integration needs it: events %q", ws.log())
	}
}

func TestRunner_setupRed_serviceNeverStarted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		failed string
		red    string
	}{
		{name: "setup red", failed: "setup[1]", red: "go install lint@v2"},
		{name: "earlier gate red", failed: "build", red: "make build"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			script := servicesScript()
			script[tt.red] = scripted{exit: 1}
			ws := &fakeWorkspace{script: script, services: ready()}

			report := run(t, ws, servicesContract(build, integration))
			if report.FailedStep != tt.failed {
				t.Fatalf("FailedStep = %q, want %q", report.FailedStep, tt.failed)
			}
			for _, e := range ws.log() {
				if strings.HasPrefix(e, "start ") {
					t.Fatalf("events = %q: a service started for a gate that was skipped", ws.log())
				}
			}
			assertResult(t, report.Gates[1], "make integration", domain.StepSkipped, domain.NoExitCode, "", "")
		})
	}
}

func TestRunner_serviceNotReady_gateFailedWithReasonAndServiceLog_laterGatesSkipped(t *testing.T) {
	t.Parallel()
	const reason = "service postgres not healthy within 1m0s"
	services := ready()
	services["postgres"] = scriptedService{notReady: true, reason: reason, log: "FATAL: data directory has wrong ownership\n", truncated: true}
	ws := &fakeWorkspace{script: servicesScript(), services: services}

	report := run(t, ws, servicesContract(build, integration, test))
	if report.FailedStep != "integration" || report.OK() {
		t.Fatalf("FailedStep = %q, OK = %v; want integration and not OK", report.FailedStep, report.OK())
	}
	got := report.Gates[1]
	assertResult(t, got, "make integration", domain.StepFailed, domain.NoExitCode, "FATAL: data directory has wrong ownership\n", "")
	if got.Reason != reason || !got.Truncated {
		t.Errorf("integration = {reason %q, truncated %v}, want {%q, true}: both are the service's", got.Reason, got.Truncated, reason)
	}
	if got.Duration <= 0 {
		t.Errorf("Duration = %v, want the time the start took", got.Duration)
	}
	for _, cmd := range ws.ran() {
		if cmd == "make integration" || cmd == "make test" {
			t.Errorf("commands ran = %q: %s ran after its service was not ready", ws.ran(), cmd)
		}
	}
	assertResult(t, report.Gates[2], "make test", domain.StepSkipped, domain.NoExitCode, "", "")
}

func TestRunner_firstOfTwoNeedsNotReady_secondNeverStartedGateFailed(t *testing.T) {
	t.Parallel()
	services := ready()
	services["postgres"] = scriptedService{notReady: true, reason: "service postgres exited with code 1"}
	ws := &fakeWorkspace{script: servicesScript(), services: services}
	e2e := domain.Gate{Name: "e2e", Run: "make e2e", Needs: []string{"postgres", "redis"}}

	report := run(t, ws, servicesContract(e2e))
	want := append(append([]string{}, setupEvents...), "start postgres", "close")
	if got := ws.log(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %q, want %q: nothing after the first service that is not ready", got, want)
	}
	if got := report.Gates[0]; got.Status != domain.StepFailed || got.Reason != "service postgres exited with code 1" {
		t.Fatalf("e2e = {%s, reason %q}, want failed with postgres's reason", got.Status, got.Reason)
	}
}

func TestRunner_runDeadlineDuringServiceStart_gateTimedOut(t *testing.T) {
	t.Parallel()
	services := ready()
	services["postgres"] = scriptedService{block: true, log: "initdb: waiting\n"}
	ws := &fakeWorkspace{script: servicesScript(), services: services}
	runner, _ := newRunner(ws)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	report, err := runner.Run(ctx, testRunID, servicesContract(build, integration, test), "/tree")
	if err != nil {
		t.Fatalf("Run: %v, want nil: the run's own deadline is a result", err)
	}
	got := report.Gates[1]
	if got.Status != domain.StepTimedOut || got.ExitCode != domain.NoExitCode || report.FailedStep != "integration" {
		t.Fatalf("integration = {%s exit %d}, FailedStep %q; want {timed_out -1}, integration", got.Status, got.ExitCode, report.FailedStep)
	}
	if report.Gates[2].Status != domain.StepSkipped || ws.closes() != 1 {
		t.Errorf("test = %s, Close called %d times; want skipped and once", report.Gates[2].Status, ws.closes())
	}
}

func TestRunner_serviceStartFailsToRun_isAnError(t *testing.T) {
	t.Parallel()
	services := ready()
	services["postgres"] = scriptedService{err: errors.New("daemon: pull access denied")}
	ws := &fakeWorkspace{script: servicesScript(), services: services}
	runner, _ := newRunner(ws)

	_, err := runner.Run(context.Background(), testRunID, servicesContract(build, integration), "/tree")
	if err == nil {
		t.Fatal("Run = nil error, want the start failure: it is not a verdict on the repository")
	}
	for _, want := range []string{"integration", "postgres", "pull access denied"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if ws.closes() != 1 {
		t.Errorf("Close called %d times, want exactly once", ws.closes())
	}
}

func TestRunner_needNotDeclared_isAnErrorNothingStarted(t *testing.T) {
	t.Parallel()
	ws := &fakeWorkspace{script: servicesScript(), services: ready()}
	runner, _ := newRunner(ws)
	ghost := domain.Gate{Name: "integration", Run: "make integration", Needs: []string{"ghost"}}

	_, err := runner.Run(context.Background(), testRunID, servicesContract(ghost), "/tree")
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("Run = %v, want an error naming the undeclared service", err)
	}
	if _, ok := ws.started("ghost"); ok {
		t.Errorf("a service the contract does not declare was started")
	}
}

func TestRunner_readinessZero_workspaceGetsDefault(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		readiness time.Duration
		want      time.Duration
	}{
		{name: "zero", readiness: 0, want: 60 * time.Second},
		{name: "set", readiness: 5 * time.Second, want: 5 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ws := &fakeWorkspace{script: servicesScript(), services: ready()}
			runner := &gates.Runner{Launcher: &fakeLauncher{ws: ws}, Now: ticking(), Readiness: tt.readiness}

			if _, err := runner.Run(context.Background(), testRunID, servicesContract(integration), "/tree"); err != nil {
				t.Fatalf("Run: %v", err)
			}
			call, _ := ws.started("postgres")
			if call.readiness != tt.want {
				t.Fatalf("readiness given to the workspace = %v, want %v", call.readiness, tt.want)
			}
		})
	}
}

func TestRunner_serviceEnv_passedFromTheContract(t *testing.T) {
	t.Parallel()
	ws := &fakeWorkspace{script: servicesScript(), services: ready()}

	run(t, ws, servicesContract(integration))
	call, ok := ws.started("postgres")
	if !ok || !reflect.DeepEqual(call.svc, postgres) {
		t.Fatalf("postgres started with %+v, want the contract's %+v", call.svc, postgres)
	}
}

// stepClock reads one second later every time, and jumps further when a
// service takes its time to start.
type stepClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *stepClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(time.Second)
	return c.t
}

func (c *stepClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestRunner_gateWithNeeds_durationCoversServiceStart(t *testing.T) {
	t.Parallel()
	clock := &stepClock{t: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}
	ws := &fakeWorkspace{script: servicesScript(), services: ready(), onStart: func() { clock.advance(10 * time.Second) }}
	runner := &gates.Runner{Launcher: &fakeLauncher{ws: ws}, Now: clock.now}

	report, err := runner.Run(context.Background(), testRunID, servicesContract(build, integration), "/tree")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := report.Gates[1].Duration; got != 11*time.Second {
		t.Fatalf("integration Duration = %v, want 11s: the 10s start plus the clock's one second", got)
	}
	if got := report.Gates[0].Duration; got != time.Second {
		t.Errorf("build Duration = %v, want 1s: no service of its own", got)
	}
}
