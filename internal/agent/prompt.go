package agent

import (
	"fmt"
	"strings"
)

// PromptPath is where the prompt is written in the workspace: outside the
// tree, so the agent never commits it.
const PromptPath = "/forja/prompt.md"

// The fixed identity of the agent's commits. It names no person.
const (
	gitName  = "Forja agent"
	gitEmail = "agent@forja.invalid"
)

// tools is what the agent may use. It is both offered and allowed: in
// dontAsk mode a tool that is only offered is denied.
const tools = "Read,Edit,Write,Bash,Glob,Grep"

// Command is the agent's invocation. Every value in it is fixed; the
// prompt, which carries untrusted text, arrives on stdin from PromptPath,
// so nothing is quoted for the shell. The tree is copied in owned by
// another user than the agent's, so git is told to trust it.
func Command() string {
	return strings.Join([]string{
		"GIT_AUTHOR_NAME='" + gitName + "'", "GIT_AUTHOR_EMAIL=" + gitEmail,
		"GIT_COMMITTER_NAME='" + gitName + "'", "GIT_COMMITTER_EMAIL=" + gitEmail,
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=safe.directory", "GIT_CONFIG_VALUE_0=/workspace",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"claude -p --bare --output-format json --no-session-persistence",
		"--permission-mode dontAsk --permission-prompts none",
		"--tools " + tools, "--allowedTools " + tools,
		"< " + PromptPath,
	}, " ")
}

// Render is the agent's prompt: the issue, the agent_context files in
// order, the gates its commits will face, and how to deliver.
func Render(t Task) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Issue #%d: %s\n\n%s\n\n", t.Issue.Number, t.Issue.Title, strings.TrimRight(t.Issue.Body, "\n"))
	if len(t.Context) > 0 {
		b.WriteString("## Repository context\n\nThese files are the repository's own guidance, read from the base commit.\n\n")
		for _, f := range t.Context {
			fmt.Fprintf(&b, "### %s\n\n", f.Path)
			quote(&b, f.Content)
		}
	}
	if len(t.Contract.Gates) > 0 {
		b.WriteString("## What will be checked\n\nOnce you are done, the control plane runs each of these commands on your commits; each must exit 0.\n\n")
		for _, g := range t.Contract.Gates {
			fmt.Fprintf(&b, "### %s\n\n", g.Name)
			quote(&b, g.Run)
		}
	}
	fmt.Fprintf(&b, "## How to deliver\n\nWork in /workspace, on the branch %s, which is checked out. "+
		"Commit your changes with git on that branch as you go; do not push, and do not change the remotes. "+
		"Only your commits on that branch are kept.\n", t.Branch)
	return []byte(b.String())
}

// quote writes text in a fenced block whose fence is longer than any run
// of backticks inside it, so the text cannot close it.
func quote(b *strings.Builder, text string) {
	fence := strings.Repeat("`", max(3, longestRun(text, '`')+1))
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	fmt.Fprintf(b, "%s\n%s%s\n\n", fence, text, fence)
}

func longestRun(s string, c byte) int {
	longest, run := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] != c {
			run = 0
			continue
		}
		run++
		longest = max(longest, run)
	}
	return longest
}
