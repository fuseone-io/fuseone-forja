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
	if j.Dir == "" {
		return errors.New("claim journal has no directory")
	}
	if err := os.MkdirAll(j.Dir, 0o700); err != nil {
		return fmt.Errorf("claim journal: %w", err)
	}
	body, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("claim journal: %w", err)
	}
	tmp, err := os.CreateTemp(j.Dir, ".entry-*.tmp")
	if err != nil {
		return fmt.Errorf("claim journal: %w", err)
	}
	if err := writeAndClose(tmp, body); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("claim journal: %w", err)
	}
	if err := os.Rename(tmp.Name(), j.path(e.Repository, e.Issue)); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("claim journal: %w", err)
	}
	return nil
}

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
