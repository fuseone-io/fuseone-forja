package workspace_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/agent"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/workspace"
)

// e2eLauncher adapts the Docker driver to the agent runner, and reads the
// tree's git log just before the runner halts the workspace.
type e2eLauncher struct {
	docker *workspace.Docker
	log    *string
}

func (l e2eLauncher) LaunchAgent(ctx context.Context, in agent.Launch) (agent.Workspace, error) {
	c, err := l.docker.LaunchAgent(ctx, workspace.AgentLaunch(in))
	if err != nil {
		return nil, err
	}
	return e2eWorkspace{Container: c, log: l.log}, nil
}

type e2eWorkspace struct {
	*workspace.Container
	log *string
}

func (w e2eWorkspace) Exec(ctx context.Context, command string) (agent.ExecResult, error) {
	res, err := w.Container.Exec(ctx, command)
	return agent.ExecResult(res), err
}

func (w e2eWorkspace) Model(ctx context.Context) (agent.ModelReport, error) {
	r, err := w.Container.Model(ctx)
	return agent.ModelReport(r), err
}

func (w e2eWorkspace) Halt(ctx context.Context) error {
	res, err := w.Container.Exec(ctx, "git -c safe.directory=/workspace log --format='%an <%ae> %s'")
	if err == nil {
		*w.log = string(res.Stdout)
	}
	return w.Container.Halt(ctx)
}

// gitTree is a one-commit repository, the shape of the control plane's
// clone.
func gitTree(t *testing.T) string {
	t.Helper()
	dir := treeWith(t)
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "."},
		{"-c", "user.name=Base", "-c", "user.email=base@forja.invalid", "commit", "-q", "-m", "base"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func skipWithoutRealAgent(t *testing.T) {
	t.Helper()
	if os.Getenv("FORJA_AGENT_TEST") != "1" || os.Getenv("FORJA_AGENT_BINARY") == "" {
		t.Skip("set FORJA_AGENT_TEST=1 and FORJA_AGENT_BINARY to a linux claude to run the real agent")
	}
}

// The real agent, against a fake model scripted with one Bash turn that
// commits, then an end_turn: it exits 0, the commit is in the tree under
// the agent's identity, and the run's tokens are exactly what the fake
// served. It needs a linux claude for the daemon's architecture in
// FORJA_AGENT_BINARY and FORJA_AGENT_TEST=1; no key is involved.
func TestAgent_realClaudeAgainstFake_commitsAndTokensMetered(t *testing.T) {
	skipWithoutRealAgent(t)
	upstream, fakeID := fakeUpstream(t, "echo forja > proof.txt && git add proof.txt && git commit -q -m 'forja proof'")
	d := agentDocker(t, upstream, 1_000_000, os.Getenv("FORJA_AGENT_BINARY"))
	var gitLog string
	runner := &agent.Runner{Launcher: e2eLauncher{docker: d, log: &gitLog}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	co := controlPlaneClone(t)
	res, err := runner.Run(ctx, agent.Task{
		RunID: newRunID(t), Tree: co.Dir(),
		Contract: domain.Contract{Image: agentImage, Gates: []domain.Gate{{Name: "build", Run: "go build ./..."}}},
		Issue:    domain.Issue{Number: 1, Title: "Write a proof file", Body: "Commit proof.txt."},
		Branch:   exportBranch, BaseSHA: co.SHA(), Bundle: filepath.Join(t.TempDir(), "commits.bundle"),
	})
	if err != nil || res.ExitCode != 0 || res.TimedOut || res.Capped || !res.Exported {
		t.Fatalf("Run = %+v, %v; want exit 0 with the commits exported", res, err)
	}
	if !strings.HasPrefix(gitLog, "Forja agent <agent@forja.invalid> forja proof\n") || !strings.Contains(gitLog, "base") {
		t.Fatalf("git log in the workspace =\n%s\nwant the agent's commit on top of the base", gitLog)
	}
	if served := fakeServed(t, fakeID); res.Tokens != served || served.Total() == 0 {
		t.Fatalf("metered %+v, want what the fake served %+v", res.Tokens, served)
	}
	t.Logf("tokens %+v, egress denied %d %+v", res.Tokens, res.Egress.Count, res.Egress.Denied)
}
