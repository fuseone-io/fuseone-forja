package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// stateLockFile is the file under the state directory whose lock marks
// the directory as one serve's.
const stateLockFile = "forjad.lock"

// errStateDirLocked is a state directory another forjad serves from.
var errStateDirLocked = errors.New("state directory is held by another forjad")

// lockStateDir makes dir this process's alone, creating it when missing,
// with an exclusive lock that does not wait: one already held is
// errStateDirLocked naming dir. The returned func lets go of it; the
// kernel lets go of it too when the process dies, so nothing is left to
// clean up after a crash. The lock belongs to the open file description,
// and the descriptor is close-on-exec, so no child process keeps it.
func lockStateDir(dir string) (release func(), err error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("state directory %s: %w", dir, err)
	}
	f, err := os.OpenFile(filepath.Join(dir, stateLockFile), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("state directory %s: %w", dir, err)
	}
	if err := flockExclusive(f); err != nil {
		// The lock failure is the one to report; nothing was written.
		_ = f.Close()
		return nil, fmt.Errorf("%s %s: %w", EnvStateDir, dir, err)
	}
	// Closing the descriptor is what releases the lock; there is nothing
	// a failed close could still lose. The func keeps f reachable: an
	// unreachable *os.File is closed by its finalizer, which would drop
	// the lock while serve still runs.
	return func() { _ = f.Close() }, nil
}

// holdStateDir locks the configured state directory for the life of the
// caller; with none configured there is no journal to share and nothing
// to lock.
func holdStateDir(getenv func(string) string) (release func(), err error) {
	dir := getenv(EnvStateDir)
	if dir == "" {
		return func() {}, nil
	}
	return lockStateDir(dir)
}

func flockExclusive(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return errStateDirLocked
	}
	if err != nil {
		return fmt.Errorf("lock %s: %w", f.Name(), err)
	}
	return nil
}
