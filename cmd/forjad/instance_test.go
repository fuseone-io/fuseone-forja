package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// One state directory is one instance on every start; another directory
// is another instance.
func TestInstanceOwner_stateDir_sameOnEveryStartOtherForAnotherDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first, err := instanceOwner(envOf(map[string]string{EnvStateDir: dir}))
	if err != nil || first == "" {
		t.Fatalf("instanceOwner = %q, %v; want an id", first, err)
	}
	again, err := instanceOwner(envOf(map[string]string{EnvStateDir: dir}))
	if err != nil || again != first {
		t.Errorf("instanceOwner of the same directory = %q, %v; want %q again", again, err, first)
	}
	other, err := instanceOwner(envOf(map[string]string{EnvStateDir: t.TempDir()}))
	if err != nil || other == "" || other == first {
		t.Errorf("instanceOwner of another directory = %q, %v; want an id other than %q", other, err, first)
	}
}

// A link planted where the id is written does not redirect the write: the
// file it points at keeps its content and the id is still recorded.
func TestInstanceOwner_linkPlantedAtTheTemporaryName_notFollowed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, instanceFile+".tmp")); err != nil {
		t.Fatal(err)
	}

	owner, err := instanceOwner(envOf(map[string]string{EnvStateDir: dir}))

	if got, _ := os.ReadFile(victim); string(got) != "keep" {
		t.Errorf("the linked file now holds %q, want it untouched", got)
	}
	if again, _ := instanceOwner(envOf(map[string]string{EnvStateDir: dir})); err != nil || owner == "" || again != owner {
		t.Errorf("instanceOwner = %q, %v, then %q; want one id recorded", owner, err, again)
	}
}

// With no state directory there is no instance to own anything.
func TestInstanceOwner_noStateDir_none(t *testing.T) {
	t.Parallel()
	if owner, err := instanceOwner(emptyEnv); owner != "" || err != nil {
		t.Fatalf("instanceOwner = %q, %v; want none", owner, err)
	}
}

// An instance file that is not an id fails the start, naming the file,
// instead of sweeping under whatever it holds.
func TestInstanceOwner_fileNotAnID_fails(t *testing.T) {
	t.Parallel()
	for name, content := range map[string]string{
		"empty":     "",
		"short":     "0123abcd",
		"not hex":   strings.Repeat("z", 32),
		"a newline": strings.Repeat("a", 32) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, instanceFile)
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			owner, err := instanceOwner(envOf(map[string]string{EnvStateDir: dir}))
			if err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("instanceOwner = %q, %v; want an error naming %s", owner, err, path)
			}
		})
	}
}

// Every object a workspace creates is labelled with the instance of the
// state directory, and with none when there is no directory.
func TestWorkspaceOptions_owner_theStateDirsInstance(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := envOf(map[string]string{EnvStateDir: dir, EnvWorkspaceRuntime: "runc"})
	opts, err := workspaceOptions(env, io.Discard)
	if err != nil {
		t.Fatalf("workspaceOptions: %v", err)
	}
	want, _ := instanceOwner(env)
	if opts.Owner == "" || opts.Owner != want {
		t.Errorf("Options.Owner = %q, want the instance %q", opts.Owner, want)
	}
	opts, err = workspaceOptions(quietEnv, io.Discard)
	if err != nil || opts.Owner != "" {
		t.Errorf("Options.Owner with no state directory = %q, %v; want none", opts.Owner, err)
	}
}
