package registry_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/proxy/registry"
)

const (
	readyLine   = `{"event":"ready","run":"run-registry-1","at":"2026-11-02T10:00:00Z","addr":"[::]:8080"}`
	allowedLine = `{"event":"access","run":"run-registry-1","at":"2026-11-02T10:00:01Z","registry":"ghcr.io/my-org","path":"/v2/x","outcome":"allowed","status":200}`
	deniedLine  = `{"event":"access","run":"run-registry-1","at":"2026-11-02T10:00:02Z","registry":"example.com","path":"/x","outcome":"denied","status":403}`
)

func TestReadAccesses_proxyLog_everyAccessInOrder(t *testing.T) {
	t.Parallel()
	report, err := registry.ReadAccesses(strings.NewReader(readyLine+"\n"+allowedLine+"\n"+deniedLine+"\n"), testRun)
	if err != nil {
		t.Fatalf("ReadAccesses: %v", err)
	}
	at := time.Date(2026, 11, 2, 10, 0, 1, 0, time.UTC)
	want := domain.RegistryReport{Count: 2, Accesses: []domain.RegistryAccess{
		{At: at, Registry: "ghcr.io/my-org", Path: "/v2/x", Outcome: domain.RegistryAllowed, Status: 200},
		{At: at.Add(time.Second), Registry: "example.com", Path: "/x", Outcome: domain.RegistryDenied, Status: 403},
	}}
	if !reflect.DeepEqual(report, want) {
		t.Fatalf("report = %+v, want %+v", report, want)
	}
}

func TestRegistryLog_lineOfOtherRun_refused(t *testing.T) {
	t.Parallel()
	other := strings.Replace(deniedLine, testRun, "run-other", 1)
	if report, err := registry.ReadAccesses(strings.NewReader(readyLine+"\n"+other+"\n"), testRun); err == nil {
		t.Fatalf("ReadAccesses with another run's line = %+v, nil error; want the log refused", report)
	}
}

func TestRegistryLog_unknownLine_refused(t *testing.T) {
	t.Parallel()
	for name, bad := range map[string]string{
		"not json":        "panic: runtime error",
		"unknown event":   `{"event":"denied","run":"run-registry-1","at":"2026-11-02T10:00:02Z"}`,
		"unknown outcome": strings.Replace(deniedLine, `"denied"`, `"maybe"`, 1),
		"no status":       strings.Replace(allowedLine, `,"status":200`, "", 1),
	} {
		if report, err := registry.ReadAccesses(strings.NewReader(readyLine+"\n"+bad+"\n"), testRun); err == nil {
			t.Errorf("%s: ReadAccesses = %+v, nil error; want the log refused", name, report)
		}
	}
}

func TestAwaitReady_readinessOfThisRun(t *testing.T) {
	t.Parallel()
	if err := registry.AwaitReady(strings.NewReader(readyLine+"\n"), testRun); err != nil {
		t.Errorf("AwaitReady(own readiness) = %v", err)
	}
	if err := registry.AwaitReady(strings.NewReader(readyLine+"\n"), "run-other"); err == nil {
		t.Error("AwaitReady(another run's readiness) = nil, want an error")
	}
	if err := registry.AwaitReady(strings.NewReader(""), testRun); err == nil {
		t.Error("AwaitReady(empty) = nil, want an error")
	}
}
