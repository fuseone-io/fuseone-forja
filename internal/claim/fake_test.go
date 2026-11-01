package claim_test

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/fuseone-io/fuseone-forja/internal/github"
)

// fakeRefs is an in-memory repository of refs and commits behind the refs
// interface the store consumes. It enforces what the API enforces: a ref
// is created only if absent, moved only to a descendant of its current
// commit, deleted only if present, and a file is read only at a commit
// that exists.
type fakeRefs struct {
	mu      sync.Mutex
	refs    map[string]string
	commits map[string]fakeCommit
	calls   []string
	// fail makes the named operation answer this error.
	fail map[string]error
	// beforeUpdate runs once, outside the lock, before the next update is
	// applied: a window for another store to move the ref first.
	beforeUpdate func()
}

type fakeCommit struct {
	parent string
	files  map[string][]byte
}

func newFakeRefs() *fakeRefs {
	return &fakeRefs{refs: map[string]string{}, commits: map[string]fakeCommit{}, fail: map[string]error{}}
}

func key(repo, ref string) string { return repo + " " + ref }

func (f *fakeRefs) record(op string) error {
	f.calls = append(f.calls, op)
	return f.fail[op]
}

func (f *fakeRefs) GetRef(_ context.Context, repo, ref string) (github.RefObject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("GetRef"); err != nil {
		return github.RefObject{}, err
	}
	sha, ok := f.refs[key(repo, ref)]
	if !ok {
		return github.RefObject{}, fmt.Errorf("fake: %s: %w", ref, github.ErrRefMissing)
	}
	return github.RefObject{SHA: sha, Type: "commit"}, nil
}

func (f *fakeRefs) CreateRef(_ context.Context, repo, ref, sha string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("CreateRef"); err != nil {
		return err
	}
	if _, exists := f.refs[key(repo, ref)]; exists {
		return fmt.Errorf("fake: %s: %w", ref, github.ErrRefExists)
	}
	if _, known := f.commits[sha]; !known {
		return fmt.Errorf("fake: %s: commit %s does not exist", ref, sha)
	}
	f.refs[key(repo, ref)] = sha
	return nil
}

func (f *fakeRefs) UpdateRef(_ context.Context, repo, ref, sha string) error {
	f.mu.Lock()
	hook := f.beforeUpdate
	f.beforeUpdate = nil
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("UpdateRef"); err != nil {
		return err
	}
	current, ok := f.refs[key(repo, ref)]
	if !ok {
		return fmt.Errorf("fake: %s: %w", ref, github.ErrRefMissing)
	}
	if !f.descends(sha, current) {
		return fmt.Errorf("fake: %s: %w", ref, github.ErrNotFastForward)
	}
	f.refs[key(repo, ref)] = sha
	return nil
}

func (f *fakeRefs) descends(sha, ancestor string) bool {
	for sha != "" {
		if sha == ancestor {
			return true
		}
		sha = f.commits[sha].parent
	}
	return false
}

func (f *fakeRefs) DeleteRef(_ context.Context, repo, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("DeleteRef"); err != nil {
		return err
	}
	if _, ok := f.refs[key(repo, ref)]; !ok {
		return fmt.Errorf("fake: %s: %w", ref, github.ErrRefMissing)
	}
	delete(f.refs, key(repo, ref))
	return nil
}

func (f *fakeRefs) CommitFile(_ context.Context, _ string, file github.FileCommit) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("CommitFile"); err != nil {
		return "", err
	}
	if file.Parent != "" {
		if _, known := f.commits[file.Parent]; !known {
			return "", fmt.Errorf("fake: parent %s does not exist", file.Parent)
		}
	}
	return f.store(file.Parent, map[string][]byte{file.Path: file.Body}, file.Message), nil
}

func (f *fakeRefs) store(parent string, files map[string][]byte, message string) string {
	var content strings.Builder
	content.WriteString(parent + "\x00" + message)
	for path, body := range files {
		content.WriteString("\x00" + path + "\x00" + string(body))
	}
	sum := sha1.Sum([]byte(content.String()))
	sha := hex.EncodeToString(sum[:])
	f.commits[sha] = fakeCommit{parent: parent, files: files}
	return sha
}

func (f *fakeRefs) ReadFile(_ context.Context, _ string, sha, path string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.record("ReadFile"); err != nil {
		return nil, err
	}
	commit, ok := f.commits[sha]
	if !ok {
		return nil, fmt.Errorf("fake: no commit %s", sha)
	}
	body, ok := commit.files[path]
	if !ok {
		return nil, fmt.Errorf("fake: no %s at %s", path, sha)
	}
	return append([]byte(nil), body...), nil
}

// seed stores a commit with files and points ref at it, bypassing the
// API's checks, as a repository that already holds a claim would be.
func (f *fakeRefs) seed(repo, ref string, files map[string][]byte) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	sha := f.store("", files, "seed "+ref)
	f.refs[key(repo, ref)] = sha
	return sha
}

func (f *fakeRefs) refSHA(repo, ref string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sha, ok := f.refs[key(repo, ref)]
	return sha, ok
}

func (f *fakeRefs) parentOf(sha string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.commits[sha].parent
}

func (f *fakeRefs) fileAt(sha, path string) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.commits[sha].files[path]
}

func (f *fakeRefs) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}
