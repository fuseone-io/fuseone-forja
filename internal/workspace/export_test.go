package workspace_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/github"
	"github.com/fuseone-io/fuseone-forja/internal/workspace"
)

const exportBranch = "forja/5"

// controlPlaneClone is the control plane's depth-1 clone of a one-commit
// repository with the agent's branch started, as forja.implement hands it
// to the agent workspace.
func controlPlaneClone(t *testing.T) *github.Checkout {
	t.Helper()
	root := t.TempDir()
	bare := filepath.Join(root, "fuseone-io", "pilot.git")
	cmd := exec.Command("git", "clone", "-q", "--bare", gitTree(t), bare)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone --bare: %v\n%s", err, out)
	}
	cloner := &github.Cloner{Tokens: &github.StaticTokenSource{Value: "unused-over-file"}, Remote: "file://" + root + "/"}
	co, err := cloner.Clone(context.Background(), "fuseone-io/pilot", "main")
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}
	t.Cleanup(func() { _ = co.Close() })
	if err := co.StartBranch(context.Background(), exportBranch); err != nil {
		t.Fatalf("StartBranch: %v", err)
	}
	return co
}

// agentGit is git as the agent runs it in the workspace: on a tree owned
// by another user, under its own identity.
const agentGit = "git -c safe.directory=/workspace -c user.name='Forja agent' -c user.email=agent@forja.invalid"

// agentCommits runs script in the workspace, then commits everything it
// staged and returns the head.
func agentCommits(t *testing.T, c *workspace.Container, script string) string {
	t.Helper()
	res := mustExec(t, c, script+" && "+agentGit+" commit -q -m work && "+agentGit+" rev-parse HEAD")
	if res.ExitCode != 0 {
		t.Fatalf("agent commit: exit %d\n%s", res.ExitCode, res.Stderr)
	}
	return strings.TrimSpace(string(res.Stdout))
}

// The agent's commit comes out of the workspace as a bundle the control
// plane's clone adopts, and nothing else does: not a file it left
// uncommitted. A branch still at the base has nothing to export, a bundle
// over the cap is refused, and Close leaves nothing of the run.
func TestDocker_exportCommits_bundleOfAgentCommitOnly(t *testing.T) {
	upstream, _ := fakeUpstream(t, "")
	runID := newRunID(t)
	co := controlPlaneClone(t)
	c := launchAgentOn(t, agentDocker(t, upstream, 1_000_000, agentBinaryFile(t)), runID, co.Dir())
	ctx := context.Background()
	tmp := t.TempDir()

	err := c.ExportCommits(ctx, co.SHA(), exportBranch, filepath.Join(tmp, "empty.bundle"))
	if !errors.Is(err, domain.ErrNoCommits) {
		t.Fatalf("ExportCommits of a branch at the base = %v, want ErrNoCommits", err)
	}
	err = c.ExportCommits(ctx, co.SHA(), "forja-qa/4", filepath.Join(tmp, "missing.bundle"))
	if !errors.Is(err, domain.ErrNoBranch) || errors.Is(err, domain.ErrNoCommits) {
		t.Fatalf("ExportCommits of a branch the tree lacks = %v, want ErrNoBranch", err)
	}
	head := agentCommits(t, c, "echo proof > proof.txt && echo left > leftover.txt && "+agentGit+" add proof.txt")
	dst := filepath.Join(tmp, "commits.bundle")
	if err := c.ExportCommits(ctx, co.SHA(), exportBranch, dst); err != nil {
		t.Fatalf("ExportCommits: %v", err)
	}
	if got, err := co.Adopt(ctx, dst, exportBranch); err != nil || got != head {
		t.Fatalf("Adopt = %s, %v; want the workspace's head %s", got, err, head)
	}
	if b, err := os.ReadFile(filepath.Join(co.Dir(), "proof.txt")); err != nil || string(b) != "proof\n" {
		t.Fatalf("proof.txt in the adopted tree = %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(co.Dir(), "leftover.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an uncommitted file reached the control plane: %v", err)
	}
	assertOversizeRefused(t, c, co.SHA())
	if err := c.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if n := leftovers(t, runID); n != 0 {
		t.Fatalf("%d containers or networks of the run left after Close", n)
	}
}

// assertOversizeRefused commits incompressible bytes past the cap and
// checks the export is refused with nothing written.
func assertOversizeRefused(t *testing.T, c *workspace.Container, base string) {
	t.Helper()
	agentCommits(t, c, "head -c "+strconv.Itoa(workspace.MaxBundleBytes+1<<20)+" /dev/urandom > big.bin && "+agentGit+" add big.bin")
	dst := filepath.Join(t.TempDir(), "big.bundle")
	if err := c.ExportCommits(context.Background(), base, exportBranch, dst); err == nil {
		t.Fatal("ExportCommits of a bundle over the cap = nil, want a refusal")
	}
	if _, err := os.Lstat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an oversized bundle left %s behind: %v", dst, err)
	}
}
