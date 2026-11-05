package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/claim"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/mcp"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
	"github.com/fuseone-io/fuseone-forja/internal/runstore/pgtest"
)

// envHoldLock names the state directory the helper process locks.
const envHoldLock = "FORJA_TEST_HOLD_STATE_LOCK"

// TestHelperProcess_holdsStateLock is not a test: run by holdLockInProcess
// as a process of its own, it locks the directory, says so on stdout and
// holds the lock until its stdin closes.
func TestHelperProcess_holdsStateLock(t *testing.T) {
	dir := os.Getenv(envHoldLock)
	if dir == "" {
		t.Skip("helper process, run by holdLockInProcess")
	}
	release, err := lockStateDir(dir)
	if err != nil {
		t.Fatalf("lockStateDir: %v", err)
	}
	defer release()
	if _, err := os.Stdout.WriteString("locked\n"); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}

// holdLockInProcess starts a process of its own that holds dir's lock,
// and returns once it does. Closing the returned pipe makes it release
// the lock and exit.
func holdLockInProcess(t *testing.T, dir string) (*exec.Cmd, io.WriteCloser) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess_holdsStateLock$")
	cmd.Env = append(os.Environ(), envHoldLock+"="+dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("helper process: %q, %v; want it to report the lock held", line, err)
	}
	return cmd, stdin
}

func assertLocked(t *testing.T, dir string) {
	t.Helper()
	release, err := lockStateDir(dir)
	if err == nil {
		release()
		t.Fatal("lockStateDir took a lock another holder has")
	}
	if !errors.Is(err, errStateDirLocked) || !strings.Contains(err.Error(), dir) {
		t.Fatalf("lockStateDir = %v, want errStateDirLocked naming %s", err, dir)
	}
}

func assertFree(t *testing.T, dir string) {
	t.Helper()
	release, err := lockStateDir(dir)
	if err != nil {
		t.Fatalf("lockStateDir after the holder let go = %v, want the lock", err)
	}
	release()
}

// Two descriptions of the lock file in one process are two holders, as
// two processes are: the second is refused until the first lets go.
func TestLockStateDir_secondDescription_refusedUntilFirstReleased(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "state")
	release, err := lockStateDir(dir)
	if err != nil {
		t.Fatalf("first lockStateDir: %v", err)
	}
	assertLocked(t, dir)
	release()
	assertFree(t, dir)
}

// Another process holding the lock refuses this one; once that process
// exits, cleanly or killed, the kernel frees the lock with nothing to
// clean up.
func TestLockStateDir_heldByAnotherProcess_refusedUntilItExits(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*exec.Cmd, io.WriteCloser) error{
		"clean exit": func(cmd *exec.Cmd, stdin io.WriteCloser) error {
			if err := stdin.Close(); err != nil {
				return err
			}
			return cmd.Wait()
		},
		"killed": func(cmd *exec.Cmd, _ io.WriteCloser) error {
			if err := cmd.Process.Kill(); err != nil {
				return err
			}
			// A killed process's Wait reports the signal; the exit is all
			// the test needs.
			_ = cmd.Wait()
			return nil
		},
	}
	for name, exit := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			cmd, stdin := holdLockInProcess(t, dir)
			assertLocked(t, dir)
			if err := exit(cmd, stdin); err != nil {
				t.Fatalf("helper exit: %v", err)
			}
			assertFree(t, dir)
		})
	}
}

// journaledStateDir is a state directory whose journal holds the claim of
// run-crashed on issue 7.
func journaledStateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	journal := &claim.Journal{Dir: filepath.Join(dir, "claims")}
	if err := journal.Put(claim.Entry{Repository: crashedRepo, Issue: 7, RunID: "run-crashed", SHA: "c1a1"}); err != nil {
		t.Fatalf("journal: %v", err)
	}
	return dir
}

// serveOn runs serve over dir and dsn until ctx ends or its start fails.
func serveOn(ctx context.Context, dir, dsn string) (int, string) {
	env := envOf(map[string]string{
		"FORJA_GITHUB_TOKEN": "github_pat_dev", EnvStateDir: dir, EnvDatabaseURL: dsn, EnvWorkspaceRuntime: "runc",
	})
	_, serverTransport := sdk.NewInMemoryTransports()
	var stderr bytes.Buffer
	code := serve(ctx, serverTransport, &stderr, env)
	return code, stderr.String()
}

// runsUnderLock is the store the first serve recorded its run in, and the
// URL a second serve opens it with: memory, whose second serve sees
// nothing of the first's, and Postgres, shared by both, when the Docker
// tests run.
func runsUnderLock(t *testing.T) map[string]func(t *testing.T) (mcp.RunStore, string) {
	t.Helper()
	stores := map[string]func(t *testing.T) (mcp.RunStore, string){
		"memory": func(*testing.T) (mcp.RunStore, string) { return runstore.NewMemory(), "" },
	}
	if dockerTests() {
		srv := pgtest.Start(t)
		stores["postgres"] = func(t *testing.T) (mcp.RunStore, string) {
			dsn := srv.Database(t)
			return migratedRuns(t, dsn), dsn
		}
	}
	return stores
}

// A serve started on a state directory another serve holds refuses to
// start, naming the directory, before it reads the journal: the other
// process's run stays running and its claim journaled.
func TestServe_stateDirHeldByAnotherServe_refusedNothingInterrupted(t *testing.T) {
	t.Parallel()
	for name, open := range runsUnderLock(t) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// Live, so that a serve that skips the lock goes on to close the
			// run instead of failing on a cancelled database connection.
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			dir := journaledStateDir(t)
			runs, dsn := open(t)
			if err := runs.Put(ctx, runningRun()); err != nil {
				t.Fatalf("Put: %v", err)
			}
			release, err := lockStateDir(dir)
			if err != nil {
				t.Fatalf("the first serve's lock: %v", err)
			}
			defer release()

			code, stderr := serveOn(ctx, dir, dsn)

			if code != 1 || !strings.Contains(stderr, dir) || !strings.Contains(stderr, "another forjad") {
				t.Errorf("second serve = %d, stderr %q; want 1 naming %s as held by another forjad", code, stderr, dir)
			}
			if got, err := runs.Get(ctx, "run-crashed"); err != nil || got.State != domain.StateRunning {
				t.Errorf("the first serve's run = %q (%v), want it still running", got.State, err)
			}
			entries, err := (&claim.Journal{Dir: filepath.Join(dir, "claims")}).List()
			if err != nil || len(entries) != 1 {
				t.Errorf("journal after the refused start = %v (%v), want the first serve's claim kept", entries, err)
			}
		})
	}
}

// serve lets go of the state directory when it returns, so the next serve
// on it starts.
func TestServe_returned_stateDirFreeForTheNext(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if code, stderr := serveOn(ctx, dir, ""); code != 0 {
		t.Fatalf("serve = %d: %s", code, stderr)
	}
	assertFree(t, dir)
}
