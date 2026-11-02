package github_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/github"
)

// changesOf is what head changes against the base of co.
func changesOf(t *testing.T, co *github.Checkout, head string) (paths []string, patch string, truncated bool) {
	t.Helper()
	c, err := co.Changes(context.Background(), co.SHA(), head)
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	return c.Paths, c.Patch, c.Truncated
}

// Every path the head changes is listed in git's order, a rename as the
// deleted path and the added one, whatever the clone's configuration
// says about renames; the patch carries both sides.
func TestCheckout_changes_renameIsTwoPathsWithPatch(t *testing.T) {
	t.Parallel()
	co, head := adoptedCheckout(t, func(ws string) {
		runGit(t, ws, "mv", "docs/b.md", "docs/renamed.md")
		agentCommit(t, ws, map[string]string{"docs/a.md": "inverted\n"}, "rename and edit")
	})
	runGit(t, co.Dir(), "config", "diff.renames", "copies")

	paths, patch, truncated := changesOf(t, co, head)
	if want := []string{"docs/a.md", "docs/b.md", "docs/renamed.md"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("Paths = %q, want %q", paths, want)
	}
	for _, want := range []string{"+inverted", "deleted file mode", "new file mode", "b/docs/renamed.md"} {
		if !strings.Contains(patch, want) {
			t.Errorf("patch does not carry %q:\n%s", want, patch)
		}
	}
	if truncated || strings.Contains(patch, "rename from") {
		t.Fatalf("truncated %v, patch with rename detection:\n%s", truncated, patch)
	}
}

// Two commits that hold the same tree change nothing: an empty change,
// not an error.
func TestCheckout_changes_sameTree_empty(t *testing.T) {
	t.Parallel()
	co, head := adoptedCheckout(t, func(ws string) {
		agentCommit(t, ws, map[string]string{"x.txt": "x\n"}, "one")
	})
	c, err := co.Changes(context.Background(), head, head)
	if err != nil || len(c.Paths) != 0 || c.Patch != "" || c.Truncated {
		t.Fatalf("Changes(head, head) = %+v, %v; want empty", c, err)
	}
}

// A patch past the bound is cut there and says so; the paths are all
// listed whatever the patch's size.
func TestCheckout_changes_patchOverBound_cutAndTruncated(t *testing.T) {
	t.Parallel()
	co, head := adoptedCheckout(t, func(ws string) {
		agentCommit(t, ws, map[string]string{"big.txt": strings.Repeat("a line of the inversion\n", 4000), "z.txt": "z\n"}, "big")
	})

	paths, patch, truncated := changesOf(t, co, head)
	if !truncated || len(patch) != github.MaxPatchBytes {
		t.Fatalf("truncated %v, patch of %d bytes; want cut at %d", truncated, len(patch), github.MaxPatchBytes)
	}
	if want := []string{"big.txt", "z.txt"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("Paths = %q, want %q", paths, want)
	}
}

// Whether a file is text is the base's to say: a .gitattributes the
// candidate committed cannot hide its patch as a binary.
func TestCheckout_changes_candidateGitattributesBinary_patchStillText(t *testing.T) {
	t.Parallel()
	co, head := adoptedCheckout(t, func(ws string) {
		agentCommit(t, ws, map[string]string{".gitattributes": "* binary\n", "code.go": "package x\n"}, "hide")
	})

	_, patch, _ := changesOf(t, co, head)
	if !strings.Contains(patch, "+package x") || strings.Contains(patch, "Binary files") {
		t.Fatalf("patch under the candidate's attributes:\n%s", patch)
	}
}

// Either end that is not a full commit name is refused before git starts.
func TestCheckout_changes_notACommitName_refusedBeforeGit(t *testing.T) {
	t.Parallel()
	co, head := adoptedCheckout(t, func(ws string) {
		agentCommit(t, ws, map[string]string{"x.txt": "x\n"}, "one")
	})
	leak := filepath.Join(t.TempDir(), "leak")
	for _, bad := range []string{"", "HEAD", agentBranch, head[:12], "--output=" + leak, co.SHA() + "..HEAD"} {
		if c, err := co.Changes(context.Background(), co.SHA(), bad); err == nil {
			t.Errorf("Changes(base, %q) = %+v, nil; want a refusal", bad, c)
		}
		if c, err := co.Changes(context.Background(), bad, head); err == nil {
			t.Errorf("Changes(%q, head) = %+v, nil; want a refusal", bad, c)
		}
	}
	if _, err := os.Stat(leak); !os.IsNotExist(err) {
		t.Fatalf("git ran with an option for a commit: %v", err)
	}
}
