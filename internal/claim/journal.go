package claim

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Entry is one claim this process holds, as the journal records it.
type Entry struct {
	Repository string `json:"repository"`
	Issue      int    `json:"issue"`
	RunID      string `json:"run_id"`
	SHA        string `json:"sha"`
}

// Journal is the local record of the claims this process holds: one file
// per claim under Dir, written atomically. It is the only state that
// survives a crash, so the next start can release what the crashed run
// could not.
type Journal struct {
	Dir string
}

// Put records a claim, replacing any entry for the same issue.
func (j *Journal) Put(e Entry) error {
	tmp, err := j.stage(e)
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, j.path(e.Repository, e.Issue)); err != nil {
		// A temporary file left behind is never listed as an entry, so a
		// failed cleanup changes nothing; the rename error is the one to report.
		_ = os.Remove(tmp)
		return fmt.Errorf("claim journal: %w", err)
	}
	return nil
}

// PutNew records a claim only if the journal holds no entry for its
// issue, atomically, and reports whether it did: an entry already there is
// a claim this process holds and is kept.
func (j *Journal) PutNew(e Entry) (bool, error) {
	tmp, err := j.stage(e)
	if err != nil {
		return false, err
	}
	// The temporary name is never listed as an entry, so a failed cleanup
	// changes nothing.
	defer func() { _ = os.Remove(tmp) }()
	err = os.Link(tmp, j.path(e.Repository, e.Issue))
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim journal: %w", err)
	}
	return true, nil
}

// stage writes e, synced, to a temporary file in Dir and returns its name.
func (j *Journal) stage(e Entry) (string, error) {
	if j.Dir == "" {
		return "", errors.New("claim journal has no directory")
	}
	if err := os.MkdirAll(j.Dir, 0o700); err != nil {
		return "", fmt.Errorf("claim journal: %w", err)
	}
	body, err := json.Marshal(e)
	if err != nil {
		return "", fmt.Errorf("claim journal: %w", err)
	}
	tmp, err := os.CreateTemp(j.Dir, ".entry-*.tmp")
	if err != nil {
		return "", fmt.Errorf("claim journal: %w", err)
	}
	if err := writeAndClose(tmp, body); err != nil {
		// Same as above: the write error is the one to report.
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("claim journal: %w", err)
	}
	return tmp.Name(), nil
}

// writeAndClose reports the first failure; a close after a failed write
// or sync has nothing left to add to it.
func writeAndClose(f *os.File, body []byte) error {
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Remove forgets the claim of an issue; an absent entry is not an error.
func (j *Journal) Remove(repo string, issue int) error {
	if j.Dir == "" {
		return errors.New("claim journal has no directory")
	}
	err := os.Remove(j.path(repo, issue))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("claim journal: %w", err)
	}
	return nil
}

// List returns every recorded claim. A missing directory holds none; a
// file that cannot be decoded is an error naming it, never a claim to
// forget.
func (j *Journal) List() ([]Entry, error) {
	if j.Dir == "" {
		return nil, errors.New("claim journal has no directory")
	}
	files, err := os.ReadDir(j.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim journal: %w", err)
	}
	var entries []Entry
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".json") {
			continue
		}
		e, err := readEntry(filepath.Join(j.Dir, f.Name()))
		if err != nil {
			return nil, fmt.Errorf("claim journal: %s: %w", f.Name(), err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func readEntry(path string) (Entry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Entry{}, err
	}
	var e Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		return Entry{}, err
	}
	if e.Repository == "" || e.Issue <= 0 || e.RunID == "" || e.SHA == "" {
		return Entry{}, errors.New("entry is incomplete")
	}
	return e, nil
}

// path is <owner>__<name>__<issue>.json: flat, one file per issue of a
// repository.
func (j *Journal) path(repo string, issue int) string {
	return filepath.Join(j.Dir, strings.ReplaceAll(repo, "/", "__")+"__"+strconv.Itoa(issue)+".json")
}
