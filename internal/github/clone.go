package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// DefaultRemote is the URL prefix repositories are cloned from.
const DefaultRemote = "https://github.com/"

// waitDelay bounds how long a killed git may keep its pipes open before
// the run gives up on them.
const waitDelay = 2 * time.Second

// Cloner clones the base branch of a repository into a directory the
// control plane owns. The token minted for the repository reaches git only
// through its environment, as an http extraheader scoped to the remote:
// never in the arguments, never in the clone's own configuration.
type Cloner struct {
	Tokens TokenSource
	// Dir is where clone directories are created; empty means os.TempDir().
	Dir string
	// Remote is the URL prefix owner/name.git is appended to; empty means
	// DefaultRemote. Tests point it at file:// or a local HTTP server.
	Remote string
	// Git is the git binary; empty means "git" on PATH. The control plane
	// host must have git installed.
	Git string
}

// Clone fetches the tip of branch, or of the default branch when branch is
// empty, as a shallow single-branch clone. A failure after the directory
// was created removes it.
func (c *Cloner) Clone(ctx context.Context, repo, branch string) (*Checkout, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	if c.Tokens == nil {
		return nil, errors.New("github: Cloner has no token source")
	}
	tok, err := c.Tokens.Token(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("github: clone %s: %w", repo, err)
	}
	dir, err := os.MkdirTemp(c.Dir, "forja-plan-*")
	if err != nil {
		return nil, fmt.Errorf("github: clone %s: %w", repo, err)
	}
	remote := c.Remote
	if remote == "" {
		remote = DefaultRemote
	}
	co, err := c.cloneInto(ctx, dir, remote+owner+"/"+name+".git", branch, tok.Value)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("github: clone %s (%s): %w", repo, describeBranch(branch), err)
	}
	return co, nil
}

func (c *Cloner) cloneInto(ctx context.Context, dir, url, branch, token string) (*Checkout, error) {
	git := gitCmd{bin: c.Git}
	env := cloneEnv(url, token)
	if _, err := git.run(ctx, "", env, cloneArgs(branch, url, dir)...); err != nil {
		return nil, redact(err, token, env[len(env)-1])
	}
	return openCheckout(ctx, git, dir)
}

func describeBranch(branch string) string {
	if branch == "" {
		return "default branch"
	}
	return "branch " + branch
}

// cloneArgs disables every credential helper so only the header carries a
// credential, and asks for the branch tip alone.
func cloneArgs(branch, url, dir string) []string {
	args := []string{"-c", "credential.helper=", "clone", "--quiet", "--depth", "1", "--no-tags", "--single-branch"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	return append(args, "--", url, dir)
}

// cloneEnv is baseEnv plus the credential, as an extraheader scoped to the
// remote URL. The header is the last entry so callers can redact it.
func cloneEnv(url, token string) []string {
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	return append(baseEnv(),
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http."+url+".extraheader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic "+basic,
	)
}

// baseEnv is the process environment without any GIT_* variable, so a
// caller's GIT_DIR or configuration cannot reach the clone, plus the
// settings that keep the operator's own configuration and helpers out.
func baseEnv() []string {
	env := make([]string, 0, len(os.Environ())+6)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return append(env, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
}

// redact replaces an error that echoes the credential with one that does
// not; the header value is checked as well as the raw token.
func redact(err error, secrets ...string) error {
	text := err.Error()
	for _, s := range secrets {
		if s != "" && strings.Contains(text, s) {
			return errors.New("git failed; its output is withheld because it echoes the credential")
		}
	}
	return err
}

// gitCmd runs one git binary.
type gitCmd struct {
	bin string
}

// run executes git in dir with env and returns its stdout. git and its
// children are killed as a group when ctx ends, and that error is ctx's.
func (g gitCmd) run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	bin := g.bin
	if bin == "" {
		bin = "git"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = waitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("git %s: %w", subcommand(args), ctx.Err())
	}
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", subcommand(args), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// subcommand names the git verb in args, skipping leading -c options.
func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" {
			i++
			continue
		}
		return args[i]
	}
	return ""
}
