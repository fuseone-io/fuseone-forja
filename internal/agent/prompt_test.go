package agent_test

import (
	"strings"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/agent"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// inOrder fails unless every part appears in s, each after the one before.
func inOrder(t *testing.T, s string, parts ...string) {
	t.Helper()
	from := 0
	for _, p := range parts {
		i := strings.Index(s[from:], p)
		if i < 0 {
			t.Fatalf("prompt lacks %q after offset %d:\n%s", p, from, s)
		}
		from += i + len(p)
	}
}

func TestRender_issueContextInOrderAndGates(t *testing.T) {
	t.Parallel()
	tk := agent.Task{
		Contract: domain.Contract{Gates: []domain.Gate{{Name: "build", Run: "make build"}, {Name: "test", Run: "go test ./..."}}},
		Issue:    domain.Issue{Number: 11, Title: "Model proxy and agent", Body: "## What to build\n\nMeter tokens.\n"},
		Context: []agent.File{
			{Path: "CLAUDE.md", Content: "# Forja rules\n"},
			{Path: "docs/b.md", Content: "second file\n"},
			{Path: "docs/a.md", Content: "third file\n"},
		},
	}

	prompt := string(agent.Render(tk))
	inOrder(t, prompt,
		"#11", "Model proxy and agent", "Meter tokens.",
		"CLAUDE.md", "# Forja rules", "docs/b.md", "second file", "docs/a.md", "third file",
		"build", "make build", "test", "go test ./...",
	)
	for _, rule := range []string{"commit", "do not push"} {
		if !strings.Contains(strings.ToLower(prompt), rule) {
			t.Errorf("prompt does not say %q:\n%s", rule, prompt)
		}
	}
}

// A file that holds a fence cannot close the block it is quoted in: its
// fence is always longer than any run of backticks inside.
func TestRender_contentWithFences_quotedWhole(t *testing.T) {
	t.Parallel()
	content := "before\n````\n```go\nx\n```\n````\nafter\n"
	tk := agent.Task{Context: []agent.File{{Path: "README.md", Content: content}}}

	prompt := string(agent.Render(tk))
	if !strings.Contains(prompt, "`````\n"+content+"`````\n") {
		t.Fatalf("content not quoted whole in a longer fence:\n%s", prompt)
	}
}

func TestRender_noContextNoGates_stillRenders(t *testing.T) {
	t.Parallel()
	prompt := string(agent.Render(agent.Task{Issue: domain.Issue{Number: 3, Title: "t", Body: "b"}}))
	inOrder(t, prompt, "#3", "t", "b")
}

// The command is the invocation the measurements validated: the prompt on
// stdin from its file, the tools allowed as well as listed, no budget in
// USD, no traffic but the model's, and a fixed identity for the commits.
func TestCommand_measuredInvocation(t *testing.T) {
	t.Parallel()
	cmd := agent.Command()
	for _, want := range []string{
		"claude -p --bare", "--output-format json", "--no-session-persistence",
		"--permission-mode dontAsk", "--permission-prompts none",
		"--tools Read,Edit,Write,Bash,Glob,Grep", "--allowedTools Read,Edit,Write,Bash,Glob,Grep",
		"< " + agent.PromptPath, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"GIT_AUTHOR_NAME=", "GIT_AUTHOR_EMAIL=", "GIT_COMMITTER_NAME=", "GIT_COMMITTER_EMAIL=",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command %q lacks %q", cmd, want)
		}
	}
	for _, banned := range []string{"--max-budget-usd", "--append-system-prompt", "--dangerously-skip-permissions", "bypassPermissions"} {
		if strings.Contains(cmd, banned) {
			t.Errorf("command %q carries %q", cmd, banned)
		}
	}
	if !strings.HasSuffix(cmd, "< "+agent.PromptPath) {
		t.Errorf("command %q does not end reading the prompt: a positional argument after --tools is consumed by it", cmd)
	}
}
