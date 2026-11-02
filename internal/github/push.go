package github

import (
	"context"
	"errors"
	"fmt"
	"regexp"
)

// forjaBranch is the only destination a push may name: the branch of one
// positive issue.
var forjaBranch = regexp.MustCompile(`^forja/[1-9][0-9]*$`)

// commitSHA is a full SHA-1 or SHA-256 object name.
var commitSHA = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// Pusher pushes a commit of the control plane's clone to the run's branch
// with a token minted to write contents, scoped to the remote URL as
// Cloner scopes its own: only in git's environment, never in the
// arguments or the clone's configuration. The push is never forced and
// runs no hook.
type Pusher struct {
	Tokens TokenSource
	// Remote is the URL prefix owner/name.git is appended to; empty means
	// DefaultRemote.
	Remote string
	// Git is the git binary; empty means "git" on PATH.
	Git string
}

// PushRequest is one push: Head, a commit of the clone in Dir, to Branch
// of Repository.
type PushRequest struct {
	Repository string
	Dir        string
	Head       string
	Branch     string
}

// Push sends Head to refs/heads/Branch. Any Branch other than
// forja/<positive int>, or a Head that is not a full commit name, is
// refused before a token is minted or git starts. A branch the remote
// already holds at a commit Head does not descend from is refused by git.
func (p *Pusher) Push(ctx context.Context, req PushRequest) error {
	if !forjaBranch.MatchString(req.Branch) {
		return fmt.Errorf("github: push to %q refused: the only destination is forja/<issue>", req.Branch)
	}
	if !commitSHA.MatchString(req.Head) {
		return fmt.Errorf("github: push of %q refused: not a commit name", req.Head)
	}
	url, err := repoURL(p.Remote, req.Repository)
	if err != nil {
		return err
	}
	if p.Tokens == nil {
		return errors.New("github: Pusher has no token source")
	}
	tok, err := p.Tokens.Token(ctx, req.Repository, WriteContents)
	if err != nil {
		return fmt.Errorf("github: push %s to %s: %w", req.Branch, req.Repository, err)
	}
	env, basic := cloneEnv(url, tok.Value)
	git := gitCmd{bin: p.Git}
	if _, err := git.run(ctx, req.Dir, env, pushArgs(url, req.Head, req.Branch)...); err != nil {
		return fmt.Errorf("github: push %s to %s: %w", req.Branch, req.Repository, redact(err, tok.Value, basic))
	}
	return nil
}

// pushArgs disables credential helpers, hooks and submodule pushes, and
// names one non-forced refspec.
func pushArgs(url, head, branch string) []string {
	return []string{
		"-c", "credential.helper=", "-c", "core.hooksPath=/dev/null",
		"push", "--quiet", "--no-verify", "--recurse-submodules=no",
		"--", url, head + ":refs/heads/" + branch,
	}
}
