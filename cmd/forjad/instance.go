package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// instanceFile holds, under the state directory, the id of the instance
// that serves from it.
const instanceFile = "instance"

// instanceIDLen is the length of an id newRunID makes.
const instanceIDLen = 32

// instanceOwner is the id the instance serving from the state directory
// labels its Docker objects with, made and recorded on the directory's
// first start; with no directory there is none. The directory's lock makes
// it one live process's alone, and it outlives a crash, so the next start
// recognises the dead process's objects as its own. A file that does not
// hold an id is an error naming it.
func instanceOwner(getenv func(string) string) (string, error) {
	dir := getenv(EnvStateDir)
	if dir == "" {
		return "", nil
	}
	path := filepath.Join(dir, instanceFile)
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return recordInstance(path)
	}
	if err != nil {
		return "", fmt.Errorf("%s: instance id: %w", EnvStateDir, err)
	}
	id := string(raw)
	if _, err := hex.DecodeString(id); err != nil || len(id) != instanceIDLen {
		return "", fmt.Errorf("%s: instance id: %s does not hold one", EnvStateDir, path)
	}
	return id, nil
}

// recordInstance writes a new id to path, through a temporary file renamed
// over it, so that a crash leaves either no file or a whole id.
func recordInstance(path string) (string, error) {
	id := newRunID()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(id), 0o600); err != nil {
		return "", fmt.Errorf("%s: instance id: %w", EnvStateDir, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", fmt.Errorf("%s: instance id: %w", EnvStateDir, err)
	}
	return id, nil
}
