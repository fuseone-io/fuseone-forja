package github

import (
	"context"
	"fmt"
	"strings"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// adoptedRef is where Adopt fetches the bundle's branch: a ref of the
// control plane's own, never the branch checked out.
const adoptedRef = "refs/forja/adopted"

// noHooks keeps every hook of the clone from running: what is checked out
// may be the agent's tree.
var noHooks = []string{"-c", "core.hooksPath=/dev/null"}

// StartBranch creates the branch name at the pinned commit and checks it
// out, so the tree handed to the agent is on its own branch.
func (c *Checkout) StartBranch(ctx context.Context, name string) error {
	if err := validBranch(name); err != nil {
		return fmt.Errorf("github: start branch: %w", err)
	}
	args := append(append([]string{}, noHooks...), "checkout", "-q", "-b", name, c.sha)
	if _, err := c.git.run(ctx, c.dir, c.env, args...); err != nil {
		return fmt.Errorf("github: start branch %s: %w", name, err)
	}
	return nil
}

// Adopt imports branch from the bundle file the agent's workspace
// exported and checks its head out in Dir. The bundle is not trusted: it
// must verify against this clone, and its head must descend from Start
// and differ from it, or the import is refused with Dir untouched; a head
// that is Start wraps domain.ErrNoCommits. A history rewritten below Start
// is therefore refused. Only objects and the one ref cross over, never a
// configuration or a hook. SHA, ReadFile, ListFiles and BlobSHA still
// answer for the pinned commit afterwards.
func (c *Checkout) Adopt(ctx context.Context, bundle, branch string) (string, error) {
	if err := validBranch(branch); err != nil {
		return "", fmt.Errorf("github: adopt: %w", err)
	}
	head, err := c.fetchBundle(ctx, bundle, branch)
	if err != nil {
		return "", fmt.Errorf("github: adopt %s: %w", branch, err)
	}
	if head == c.start {
		return "", fmt.Errorf("github: adopt %s: %w", branch, domain.ErrNoCommits)
	}
	if _, err := c.git.run(ctx, c.dir, c.env, "merge-base", "--is-ancestor", c.start, head); err != nil {
		return "", fmt.Errorf("github: adopt %s: head %s does not descend from %s: %w", branch, head, c.start, err)
	}
	args := append(append([]string{}, noHooks...), "checkout", "-q", "--force", "-B", branch, head)
	if _, err := c.git.run(ctx, c.dir, c.env, args...); err != nil {
		return "", fmt.Errorf("github: adopt %s: %w", branch, err)
	}
	return head, nil
}

// fetchBundle verifies the bundle, fetches its branch into adoptedRef and
// returns the commit it names. adoptedRef is the control plane's own and
// takes whatever head the bundle names, fast-forward or not: what may be
// checked out is decided by the descent check after it.
func (c *Checkout) fetchBundle(ctx context.Context, bundle, branch string) (string, error) {
	if _, err := c.git.run(ctx, c.dir, c.env, "bundle", "verify", "-q", bundle); err != nil {
		return "", err
	}
	args := append(append([]string{}, noHooks...),
		"fetch", "-q", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules",
		bundle, "+refs/heads/"+branch+":"+adoptedRef)
	if _, err := c.git.run(ctx, c.dir, c.env, args...); err != nil {
		return "", err
	}
	out, err := c.git.run(ctx, c.dir, c.env, "rev-parse", "--verify", "--quiet", adoptedRef+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// validBranch accepts a branch name git would accept that cannot be read
// as an option either: no leading dash, and a fully qualified ref with no
// empty or dot segment, no lock suffix and none of git's forbidden bytes.
func validBranch(name string) error {
	if name == "" || strings.HasPrefix(name, "-") {
		return fmt.Errorf("branch %q is not a branch name", name)
	}
	if refusedBytes(name) {
		return fmt.Errorf("branch %q has a character git refuses", name)
	}
	for _, segment := range strings.Split(name, "/") {
		if strings.HasPrefix(segment, ".") || strings.HasSuffix(segment, ".lock") {
			return fmt.Errorf("branch %q has a segment git refuses", name)
		}
	}
	return validRef("refs/heads/" + name)
}

// refusedBytes reports a control character or any byte sequence
// git-check-ref-format refuses anywhere in a ref.
func refusedBytes(name string) bool {
	if strings.ContainsAny(name, " ~^:?*[\\\x7f") || strings.Contains(name, "@{") || strings.Contains(name, "..") {
		return true
	}
	return strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 })
}
