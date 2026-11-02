package mcp_test

import (
	"context"
	"crypto/sha1" // git names blobs by SHA-1; the fake computes the same name
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/github"
	forja "github.com/fuseone-io/fuseone-forja/internal/mcp"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

const (
	planRepo   = "fuseone-io/fuseone-forja"
	planSHA    = "a058ad9c0b1e2d3f4a5b6c7d8e9f0a1b2c3d4e5f"
	planBranch = "trunk"
	planIssue  = 7
)

var planIssueStatement = domain.Issue{Number: planIssue, Title: "forja.plan: checkout", Body: "## What to build\n\nClone the base.\n"}

// fakeCheckout is a tree pinned at one SHA. It answers with the github
// sentinels exactly as the real checkout does: a missing path, a directory
// read as a file, and files of a directory in git's tree order.
type fakeCheckout struct {
	sha    string
	branch string
	dir    string
	files  map[string]string
	dirs   map[string]bool
	// failPath makes ReadFile of that path fail with failErr, an error that
	// is none of the sentinels.
	failPath string
	failErr  error
	closed   int
	// started is the branch StartBranch created, or the one CloneBranch
	// checked out. start is the commit that branch starts from, the base
	// when empty. head is the commit Adopt imports; adoptedDir the tree Dir
	// holds once it did; adoptErr fails it.
	started    string
	start      string
	head       string
	adoptedDir string
	adoptErr   error
	adopted    []string
	// diffLines is what DiffLines counts for the adopted head; diffErr
	// fails it.
	diffLines   int
	diffErr     error
	adoptedHead string
}

// DiffLines answers, as the real count does, only for the head Adopt
// brought in.
func (f *fakeCheckout) DiffLines(_ context.Context, head string) (int, error) {
	if f.diffErr != nil {
		return 0, f.diffErr
	}
	if f.adoptedHead == "" || head != f.adoptedHead {
		return 0, fmt.Errorf("fake: diff of %q, adopted %q", head, f.adoptedHead)
	}
	return f.diffLines, nil
}

func (f *fakeCheckout) SHA() string    { return f.sha }
func (f *fakeCheckout) Branch() string { return f.branch }
func (f *fakeCheckout) Close() error   { f.closed++; return nil }

func (f *fakeCheckout) Start() string {
	if f.start != "" {
		return f.start
	}
	return f.sha
}

func (f *fakeCheckout) Dir() string {
	if len(f.adopted) > 0 {
		return f.adoptedDir
	}
	return f.dir
}

func (f *fakeCheckout) StartBranch(_ context.Context, name string) error {
	if f.started != "" {
		return fmt.Errorf("fake: branch %s already started", f.started)
	}
	f.started = name
	return nil
}

// Adopt enforces what the real import does: the bundle exists, it is the
// started branch, and a head at the start is no commit at all. Reads keep
// answering for the base.
func (f *fakeCheckout) Adopt(_ context.Context, bundle, branch string) (string, error) {
	f.adopted = append(f.adopted, bundle)
	if f.adoptErr != nil {
		return "", f.adoptErr
	}
	if _, err := os.Stat(bundle); err != nil {
		return "", fmt.Errorf("fake: bundle: %w", err)
	}
	if branch != f.started {
		return "", fmt.Errorf("fake: adopt %s, started %s", branch, f.started)
	}
	if f.head == f.Start() {
		return "", fmt.Errorf("fake: %w", domain.ErrNoCommits)
	}
	f.adoptedHead = f.head
	return f.head, nil
}

func (f *fakeCheckout) ReadFile(_ context.Context, path string) ([]byte, error) {
	path = strings.TrimSuffix(path, "/")
	if path == f.failPath {
		return nil, f.failErr
	}
	if f.dirs[path] {
		return nil, fmt.Errorf("%s: %w", path, github.ErrIsDirectory)
	}
	content, ok := f.files[path]
	if !ok {
		return nil, fmt.Errorf("%s: %w at %s", path, github.ErrPathMissing, f.sha)
	}
	return []byte(content), nil
}

func (f *fakeCheckout) ListFiles(_ context.Context, dir string) ([]string, error) {
	dir = strings.TrimSuffix(dir, "/")
	if !f.dirs[dir] {
		if _, ok := f.files[dir]; ok {
			return nil, fmt.Errorf("%s: is a blob, not a directory", dir)
		}
		return nil, fmt.Errorf("%s: %w at %s", dir, github.ErrPathMissing, f.sha)
	}
	var out []string
	for path := range f.files {
		if strings.HasPrefix(path, dir+"/") {
			out = append(out, path)
		}
	}
	// Byte order of full paths is git's tree order for the files of a tree.
	sort.Strings(out)
	return out, nil
}

func (f *fakeCheckout) BlobSHA(_ context.Context, path string) (string, error) {
	path = strings.TrimSuffix(path, "/")
	if f.dirs[path] {
		return "", fmt.Errorf("%s: %w", path, github.ErrIsDirectory)
	}
	content, ok := f.files[path]
	if !ok {
		return "", fmt.Errorf("%s: %w at %s", path, github.ErrPathMissing, f.sha)
	}
	return gitBlobSHA(content), nil
}

// gitBlobSHA names a blob as git does: sha1("blob <len>\0<content>").
func gitBlobSHA(content string) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(content), content)))
	return hex.EncodeToString(sum[:])
}

// fakeCloner hands out one checkout, or fails before any exists.
type fakeCloner struct {
	co     *fakeCheckout
	err    error
	repo   string
	branch string
	calls  int
	// log, when set, records "clone" in the run's call order.
	log *callLog
}

func (f *fakeCloner) Clone(_ context.Context, repo, branch string) (forja.Checkout, error) {
	if f.log != nil {
		f.log.add("clone")
	}
	f.calls++
	f.repo, f.branch = repo, branch
	if f.err != nil {
		return nil, f.err
	}
	return f.co, nil
}

type fakeIssues struct {
	issue  domain.Issue
	err    error
	repo   string
	number int
}

func (f *fakeIssues) Read(_ context.Context, repo string, number int) (domain.Issue, error) {
	f.repo, f.number = repo, number
	if f.err != nil {
		return domain.Issue{}, f.err
	}
	return f.issue, nil
}

// pilotCheckout is a checkout shaped like the pilot: contract, two root
// files and a docs tree with a nested directory.
func pilotCheckout(agentContext ...string) *fakeCheckout {
	return &fakeCheckout{
		sha:    planSHA,
		branch: planBranch,
		dir:    "/tmp/forja-plan-pilot",
		files: map[string]string{
			".forja.yaml":     contractWith(agentContext...),
			"CLAUDE.md":       "# Forja\n",
			"CONTRIBUTING.md": "# Contributing\n",
			"docs/b.md":       "b\n",
			"docs/a.md":       "a\n",
			"docs/sub/c.md":   "c\n",
		},
		dirs: map[string]bool{"docs": true, "docs/sub": true},
	}
}

// contractWith is the valid contract plus an agent_context list; no list
// means the key is absent.
func contractWith(entries ...string) string {
	if len(entries) == 0 {
		return string(validContract)
	}
	var b strings.Builder
	b.Write(validContract)
	b.WriteString("agent_context:\n")
	for _, e := range entries {
		b.WriteString("  - " + e + "\n")
	}
	return b.String()
}

func connectPlan(t *testing.T, cloner *fakeCloner, issues *fakeIssues) *sdk.ClientSession {
	t.Helper()
	contracts := &fakeContracts{err: errors.New("no contract reader in this test")}
	claims := &fakeClaims{calls: &callLog{}, claimErr: errors.New("no claimer in this test")}
	deps := testDeps(runstore.NewMemory(), contracts, claims)
	deps.Cloner = cloner
	deps.Issues = issues
	return connectDeps(t, deps)
}

func callPlan(t *testing.T, cs *sdk.ClientSession, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &sdk.CallToolParams{
		Name:      string(domain.ToolPlan),
		Arguments: args,
	})
	if err != nil {
		t.Fatalf("CallTool(forja.plan): protocol error %v", err)
	}
	return res
}

func planArgs(baseBranch string) map[string]any {
	args := map[string]any{"repository": planRepo, "issue": planIssue}
	if baseBranch != "" {
		args["base_branch"] = baseBranch
	}
	return args
}

// decodePlan fails the test on a tool error and returns the typed output.
func decodePlan(t *testing.T, res *sdk.CallToolResult) forja.PlanOutput {
	t.Helper()
	if res.IsError {
		t.Fatalf("IsError = true, content: %s", textOf(res))
	}
	var got forja.PlanOutput
	if err := json.Unmarshal(structuredJSON(t, res), &got); err != nil {
		t.Fatalf("decode PlanOutput: %v", err)
	}
	return got
}

func contextPaths(files []forja.ContextFile) []string {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	return paths
}

func TestPlan_validContract_returnsIssueShasAndContextInDeclaredOrder(t *testing.T) {
	t.Parallel()
	co := pilotCheckout("CONTRIBUTING.md", "CLAUDE.md")
	cloner := &fakeCloner{co: co}
	issues := &fakeIssues{issue: planIssueStatement}
	cs := connectPlan(t, cloner, issues)

	got := decodePlan(t, callPlan(t, cs, planArgs("main")))
	want := forja.PlanOutput{
		Repository:  planRepo,
		Issue:       planIssue,
		BaseBranch:  planBranch,
		BaseSHA:     planSHA,
		ContractSHA: gitBlobSHA(co.files[".forja.yaml"]),
		Title:       planIssueStatement.Title,
		Body:        planIssueStatement.Body,
		AgentContext: []forja.ContextFile{
			{Path: "CONTRIBUTING.md", Content: "# Contributing\n"},
			{Path: "CLAUDE.md", Content: "# Forja\n"},
		},
		Warnings: []string{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PlanOutput = %+v, want %+v", got, want)
	}
	if cloner.repo != planRepo || cloner.branch != "main" {
		t.Errorf("cloner asked for %q at %q, want %q at main", cloner.repo, cloner.branch, planRepo)
	}
	if issues.repo != planRepo || issues.number != planIssue {
		t.Errorf("issue reader asked for %s#%d, want %s#%d", issues.repo, issues.number, planRepo, planIssue)
	}
}

func TestPlan_baseBranchEmpty_cloneAskedForDefault(t *testing.T) {
	t.Parallel()
	cloner := &fakeCloner{co: pilotCheckout("CLAUDE.md")}
	cs := connectPlan(t, cloner, &fakeIssues{issue: planIssueStatement})

	got := decodePlan(t, callPlan(t, cs, planArgs("")))
	if cloner.branch != "" {
		t.Errorf("cloner asked for branch %q, want the default (empty)", cloner.branch)
	}
	if got.BaseBranch != planBranch {
		t.Errorf("base_branch = %q, want what the checkout reports, %q", got.BaseBranch, planBranch)
	}
}

func TestPlan_agentContextFileMissing_warningNotError(t *testing.T) {
	t.Parallel()
	cloner := &fakeCloner{co: pilotCheckout("CLAUDE.md", "MISSING.md", "CONTRIBUTING.md")}
	cs := connectPlan(t, cloner, &fakeIssues{issue: planIssueStatement})

	got := decodePlan(t, callPlan(t, cs, planArgs("")))
	if paths := contextPaths(got.AgentContext); !reflect.DeepEqual(paths, []string{"CLAUDE.md", "CONTRIBUTING.md"}) {
		t.Errorf("agent_context paths = %q, want the two present files in order", paths)
	}
	if len(got.Warnings) != 1 {
		t.Fatalf("warnings = %q, want exactly one", got.Warnings)
	}
	for _, want := range []string{"MISSING.md", planSHA, "not found"} {
		if !strings.Contains(got.Warnings[0], want) {
			t.Errorf("warning %q does not say %q", got.Warnings[0], want)
		}
	}
}

func TestPlan_agentContextDirectory_expandedToItsFilesAtItsPosition(t *testing.T) {
	t.Parallel()
	cloner := &fakeCloner{co: pilotCheckout("CLAUDE.md", "docs/", "CONTRIBUTING.md")}
	cs := connectPlan(t, cloner, &fakeIssues{issue: planIssueStatement})

	got := decodePlan(t, callPlan(t, cs, planArgs("")))
	want := []string{"CLAUDE.md", "docs/a.md", "docs/b.md", "docs/sub/c.md", "CONTRIBUTING.md"}
	if paths := contextPaths(got.AgentContext); !reflect.DeepEqual(paths, want) {
		t.Errorf("agent_context paths = %q, want %q", paths, want)
	}
	if got.AgentContext[3].Content != "c\n" {
		t.Errorf("docs/sub/c.md content = %q, want the file's bytes", got.AgentContext[3].Content)
	}
	if len(got.Warnings) != 0 {
		t.Errorf("warnings = %q, want none", got.Warnings)
	}
}

func TestPlan_agentContextBinaryFile_skippedWithWarning(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		entries []string
		binary  string
		want    []string
	}{
		{name: "declared entry", entries: []string{"CLAUDE.md", "logo.png"}, binary: "logo.png", want: []string{"CLAUDE.md"}},
		{name: "inside a directory", entries: []string{"docs/"}, binary: "docs/PRD.pdf", want: []string{"docs/a.md", "docs/b.md", "docs/sub/c.md"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			co := pilotCheckout(tt.entries...)
			co.files[tt.binary] = "%PDF-1.4\n\x00\x01\x02 binary"
			cs := connectPlan(t, &fakeCloner{co: co}, &fakeIssues{issue: planIssueStatement})

			got := decodePlan(t, callPlan(t, cs, planArgs("")))
			if paths := contextPaths(got.AgentContext); !reflect.DeepEqual(paths, tt.want) {
				t.Errorf("agent_context paths = %q, want %q", paths, tt.want)
			}
			if len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], tt.binary) || !strings.Contains(got.Warnings[0], "binary") {
				t.Errorf("warnings = %q, want one naming %s as binary", got.Warnings, tt.binary)
			}
		})
	}
}

func TestPlan_warningsAndAgentContextAlwaysOnTheWire(t *testing.T) {
	t.Parallel()
	cs := connectPlan(t, &fakeCloner{co: pilotCheckout()}, &fakeIssues{issue: planIssueStatement})

	res := callPlan(t, cs, planArgs(""))
	if res.IsError {
		t.Fatalf("IsError = true, content: %s", textOf(res))
	}
	wire := wireOf(t, res)
	for _, key := range []string{"agent_context", "warnings"} {
		if raw := string(wire[key]); raw != "[]" {
			t.Errorf("%s on the wire = %s, want an empty array", key, raw)
		}
	}
}

func TestPlan_wireKeys_snakeCase(t *testing.T) {
	t.Parallel()
	cs := connectPlan(t, &fakeCloner{co: pilotCheckout("CLAUDE.md")}, &fakeIssues{issue: planIssueStatement})

	res := callPlan(t, cs, planArgs("main"))
	if res.IsError {
		t.Fatalf("IsError = true, content: %s", textOf(res))
	}
	var got []string
	for key := range wireOf(t, res) {
		got = append(got, key)
	}
	sort.Strings(got)
	want := []string{"repository", "issue", "base_branch", "base_sha", "contract_sha", "title", "body", "agent_context", "warnings"}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wire keys = %q, want %q", got, want)
	}
	var item map[string]json.RawMessage
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(wireOf(t, res)["agent_context"], &items); err != nil || len(items) != 1 {
		t.Fatalf("agent_context = %s (%v)", wireOf(t, res)["agent_context"], err)
	}
	item = items[0]
	for _, key := range []string{"path", "content"} {
		if _, ok := item[key]; !ok {
			t.Errorf("agent_context[0].%s absent on the wire: %v", key, item)
		}
	}
}

// assertToolError checks a tool error, never a protocol error, whose text
// names the tool and every wanted fragment.
func assertToolError(t *testing.T, res *sdk.CallToolResult, wants ...string) {
	t.Helper()
	if !res.IsError {
		t.Fatalf("IsError = false, content: %s", textOf(res))
	}
	text := textOf(res)
	for _, want := range append([]string{string(domain.ToolPlan)}, wants...) {
		if !strings.Contains(text, want) {
			t.Errorf("error text %q does not contain %q", text, want)
		}
	}
}

func TestPlan_contractMissing_toolErrorNamesShaAndContractMissing(t *testing.T) {
	t.Parallel()
	co := pilotCheckout("CLAUDE.md")
	delete(co.files, ".forja.yaml")
	cs := connectPlan(t, &fakeCloner{co: co}, &fakeIssues{issue: planIssueStatement})

	assertToolError(t, callPlan(t, cs, planArgs("")), "contract missing", planSHA)
}

func TestPlan_contractInvalid_toolErrorListsDefects(t *testing.T) {
	t.Parallel()
	co := pilotCheckout("CLAUDE.md")
	co.files[".forja.yaml"] = string(invalidContract)
	cs := connectPlan(t, &fakeCloner{co: co}, &fakeIssues{issue: planIssueStatement})

	assertToolError(t, callPlan(t, cs, planArgs("")), "gates[0].run", "is required", planSHA)
}

func TestPlan_issueIsPullRequest_toolError(t *testing.T) {
	t.Parallel()
	notIssue := fmt.Errorf("github: %s#%d: %w", planRepo, 25, github.ErrNotAnIssue)
	cs := connectPlan(t, &fakeCloner{co: pilotCheckout("CLAUDE.md")}, &fakeIssues{err: notIssue})

	assertToolError(t, callPlan(t, cs, map[string]any{"repository": planRepo, "issue": 25}), "pull request", "25")
}

func TestPlan_issueNotFound_toolErrorNamesNumber(t *testing.T) {
	t.Parallel()
	missing := fmt.Errorf("github: %s#%d: %w", planRepo, 404, github.ErrIssueNotFound)
	cs := connectPlan(t, &fakeCloner{co: pilotCheckout("CLAUDE.md")}, &fakeIssues{err: missing})

	assertToolError(t, callPlan(t, cs, map[string]any{"repository": planRepo, "issue": 404}), "not found", "404")
}

func TestPlan_cloneFails_toolErrorNamesTool(t *testing.T) {
	t.Parallel()
	cloner := &fakeCloner{err: errors.New("github: clone fuseone-io/fuseone-forja (branch ghost): git clone: exit status 128")}
	issues := &fakeIssues{issue: planIssueStatement}
	cs := connectPlan(t, cloner, issues)

	assertToolError(t, callPlan(t, cs, planArgs("ghost")), "ghost", "exit status 128")
	if issues.number != 0 {
		t.Errorf("the issue was read although the clone failed")
	}
}

func TestPlan_checkoutClosedOnSuccessAndOnEveryErrorPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		co      func() *fakeCheckout
		issues  *fakeIssues
		isError bool
	}{
		{name: "success", co: func() *fakeCheckout { return pilotCheckout("CLAUDE.md") }, issues: &fakeIssues{issue: planIssueStatement}},
		{name: "contract missing", co: func() *fakeCheckout {
			co := pilotCheckout()
			delete(co.files, ".forja.yaml")
			return co
		}, issues: &fakeIssues{issue: planIssueStatement}, isError: true},
		{name: "contract invalid", co: func() *fakeCheckout {
			co := pilotCheckout()
			co.files[".forja.yaml"] = string(invalidContract)
			return co
		}, issues: &fakeIssues{issue: planIssueStatement}, isError: true},
		{name: "issue read fails", co: func() *fakeCheckout { return pilotCheckout("CLAUDE.md") }, issues: &fakeIssues{err: errors.New("status 500")}, isError: true},
		{name: "agent_context read fails", co: func() *fakeCheckout {
			co := pilotCheckout("CLAUDE.md")
			co.failPath, co.failErr = "CLAUDE.md", errors.New("git cat-file: exit status 128")
			return co
		}, issues: &fakeIssues{issue: planIssueStatement}, isError: true},
		{name: "agent_context read fails inside a directory", co: func() *fakeCheckout {
			co := pilotCheckout("docs/")
			co.failPath, co.failErr = "docs/b.md", errors.New("git cat-file: exit status 128")
			return co
		}, issues: &fakeIssues{issue: planIssueStatement}, isError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			co := tt.co()
			cs := connectPlan(t, &fakeCloner{co: co}, tt.issues)

			res := callPlan(t, cs, planArgs(""))
			if res.IsError != tt.isError {
				t.Fatalf("IsError = %v, want %v; content: %s", res.IsError, tt.isError, textOf(res))
			}
			if co.closed != 1 {
				t.Fatalf("Close called %d times, want exactly once", co.closed)
			}
		})
	}
}
