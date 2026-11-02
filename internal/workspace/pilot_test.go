package workspace_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/contract"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/gates"
	"github.com/fuseone-io/fuseone-forja/internal/proxy/egress"
	"github.com/fuseone-io/fuseone-forja/internal/workspace"
)

// pilotLauncher adapts the Docker driver, which returns its own types, to
// the interfaces the gates runner declares.
type pilotLauncher struct {
	docker *workspace.Docker
}

func (l pilotLauncher) Launch(ctx context.Context, runID, image, tree string) (gates.Workspace, error) {
	c, err := l.docker.Launch(ctx, runID, image, tree)
	if err != nil {
		return nil, err
	}
	return pilotWorkspace{c}, nil
}

type pilotWorkspace struct {
	*workspace.Container
}

func (w pilotWorkspace) Exec(ctx context.Context, command string) (gates.ExecResult, error) {
	res, err := w.Container.Exec(ctx, command)
	return gates.ExecResult(res), err
}

func (w pilotWorkspace) StartService(ctx context.Context, name string, svc domain.Service, readiness time.Duration) (gates.ServiceResult, error) {
	res, err := w.Container.StartService(ctx, name, svc, readiness)
	return gates.ServiceResult(res), err
}

// TestPilot_doctorContractOnThisRepository runs this repository's own
// contract against its own tree, the way forja.doctor will, behind a
// gateway that lets out only FORJA_EGRESS_ALLOW, and prints the report
// with every denial. It is a manual run: set FORJA_PILOT_TEST=1.
func TestPilot_doctorContractOnThisRepository(t *testing.T) {
	if os.Getenv("FORJA_PILOT_TEST") != "1" {
		t.Skip("set FORJA_PILOT_TEST=1 to run the pilot contract in Docker")
	}
	allow, err := egress.ParseAllow(os.Getenv("FORJA_EGRESS_ALLOW"))
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, ".forja.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := contract.Parse(body)
	if err != nil {
		t.Fatalf("contract.Parse: %v", err)
	}
	d := newDocker(t, allow...)
	wallClock := c.Limits.WallClock
	if wallClock == 0 {
		wallClock = gates.DefaultWallClock
	}
	ctx, cancel := context.WithTimeout(context.Background(), wallClock)
	defer cancel()

	runner := &gates.Runner{Launcher: pilotLauncher{d}, Now: time.Now}
	report, err := runner.Run(ctx, newRunID(t), c, root)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	t.Logf("allow=%q", allow)
	logReport(t, report)
}

func logReport(t *testing.T, report domain.GateReport) {
	t.Helper()
	for _, r := range append(report.Setup, report.Gates...) {
		t.Logf("%-10s %-10s exit=%-3d %8v  %s", r.Name, r.Status, r.ExitCode, r.Duration.Round(time.Millisecond), r.Command)
		if r.Status != "passed" && r.Status != "skipped" {
			t.Logf("  stdout tail: %q", tail(r.Stdout))
			t.Logf("  stderr tail: %q", tail(r.Stderr))
		}
	}
	for _, denial := range report.Egress.Denied {
		t.Logf("denied %-7s %s", denial.Kind, denial.Destination)
	}
	t.Logf("ok=%v failed_step=%q egress_denied=%d", report.OK(), report.FailedStep, report.Egress.Count)
}

func tail(b []byte) string {
	if len(b) > 400 {
		b = b[len(b)-400:]
	}
	return string(b)
}
