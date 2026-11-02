package github

import (
	"context"
	"fmt"
	"strings"
)

// startRef is where CloneBranch fetches the branch: a ref of the control
// plane's own, never the branch checked out.
const startRef = "refs/forja/start"

// CloneBranch clones base as Clone does, then fetches the tip of branch at
// depth 1 with the same credential, scoped the same way, and checks that
// branch out at its tip with hooks off. SHA and every read still answer
// for the base commit; Start is the tip. A branch that is not a branch
// name, or that is base itself, is refused before a token is minted. A
// failure after the directory was created removes it.
func (c *Cloner) CloneBranch(ctx context.Context, repo, base, branch string) (*Checkout, error) {
	if err := validBranch(branch); err != nil {
		return nil, fmt.Errorf("github: clone branch: %w", err)
	}
	if branch == base {
		return nil, fmt.Errorf("github: clone branch %s: it is the base branch", branch)
	}
	what := "branch " + branch + " on " + describeBranch(base)
	return c.cloneWith(ctx, repo, what, func(dir, url, token string) (*Checkout, error) {
		co, err := c.cloneInto(ctx, dir, url, base, token)
		if err != nil {
			return nil, err
		}
		if err := co.checkoutTip(ctx, url, branch, token); err != nil {
			return nil, err
		}
		return co, nil
	})
}

// checkoutTip fetches the tip of branch into startRef, with no tag, no
// submodule and no hook, checks the branch out there and makes it Start.
func (c *Checkout) checkoutTip(ctx context.Context, url, branch, token string) error {
	env, basic := cloneEnv(url, token)
	fetch := append(append([]string{"-c", "credential.helper="}, noHooks...),
		"fetch", "--quiet", "--depth", "1", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head",
		"--", url, "refs/heads/"+branch+":"+startRef)
	if _, err := c.git.run(ctx, c.dir, env, fetch...); err != nil {
		return redact(err, token, basic)
	}
	out, err := c.git.run(ctx, c.dir, c.env, "rev-parse", "--verify", "--quiet", startRef+"^{commit}")
	if err != nil {
		return err
	}
	tip := strings.TrimSpace(string(out))
	checkout := append(append([]string{}, noHooks...), "checkout", "-q", "--no-track", "-b", branch, tip)
	if _, err := c.git.run(ctx, c.dir, c.env, checkout...); err != nil {
		return err
	}
	c.start = tip
	return nil
}
