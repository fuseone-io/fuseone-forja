package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

// The tool schemas are a public contract: a golden changes only when someone
// runs `go test ./internal/mcp -run '^TestToolSchemas_' -update` and commits
// the result.
var updateGoldens = flag.Bool("update", false, "rewrite the tool schema goldens under testdata/tools")

const goldenDir = "testdata/tools"

// listedTools returns each tool exactly as a client decodes it from
// tools/list, in canonical JSON keyed by tool name.
func listedTools(t *testing.T) map[string][]byte {
	t.Helper()
	cs := connect(t, runstore.NewMemory())
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	tools := map[string][]byte{}
	for _, tool := range res.Tools {
		tools[tool.Name] = canonicalJSON(t, tool)
	}
	return tools
}

// canonicalJSON re-encodes v through a generic value so that every object,
// the SDK's structs included, comes out with its keys sorted.
func canonicalJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// any: the decoded tool is an arbitrary JSON document; encoding/json
	// sorts map keys, which is the stable order the goldens need.
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out, err := json.MarshalIndent(generic, "", "  ")
	if err != nil {
		t.Fatalf("marshal canonical: %v", err)
	}
	return append(out, '\n')
}

func goldenPath(name string) string {
	return filepath.Join(goldenDir, name+".json")
}

// Every field a client sees in tools/list — name, title, description,
// input and output schema, annotations — matches its committed golden.
func TestToolSchemas_listedTools_matchGoldens(t *testing.T) {
	t.Parallel()
	tools := listedTools(t)
	if *updateGoldens {
		writeGoldens(t, tools)
	}
	for name, got := range tools {
		want, err := os.ReadFile(goldenPath(name))
		if err != nil {
			t.Errorf("%s: no golden (%v); run with -update to create it", name, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: listed tool differs from %s (- golden, + listed):\n%s", name, goldenPath(name), lineDiff(string(want), string(got)))
		}
	}
}

// A golden whose tool is no longer listed fails too: removing a tool is a
// schema change like any other.
func TestToolSchemas_goldenWithoutTool_fails(t *testing.T) {
	t.Parallel()
	tools := listedTools(t)
	files, err := filepath.Glob(filepath.Join(goldenDir, "*.json"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("no goldens under %s", goldenDir)
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		if _, listed := tools[name]; !listed {
			t.Errorf("golden %s has no listed tool %q; delete it if the removal is deliberate", f, name)
		}
	}
}

func writeGoldens(t *testing.T, tools map[string][]byte) {
	t.Helper()
	if err := os.MkdirAll(goldenDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", goldenDir, err)
	}
	for name, body := range tools {
		if err := os.WriteFile(goldenPath(name), body, 0o644); err != nil {
			t.Fatalf("write %s: %v", goldenPath(name), err)
		}
	}
}

// lineDiff renders the lines of want and got that are not in their longest
// common subsequence, each with its line number in the side it came from.
func lineDiff(want, got string) string {
	a, b := strings.Split(want, "\n"), strings.Split(got, "\n")
	lcs := lcsTable(a, b)
	var out strings.Builder
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		switch {
		case i < len(a) && j < len(b) && a[i] == b[j]:
			i, j = i+1, j+1
		case i < len(a) && (j == len(b) || lcs[i+1][j] >= lcs[i][j+1]):
			fmt.Fprintf(&out, "-%4d %s\n", i+1, a[i])
			i++
		default:
			fmt.Fprintf(&out, "+%4d %s\n", j+1, b[j])
			j++
		}
	}
	return out.String()
}

// lcsTable holds at [i][j] the length of the longest common subsequence of
// a[i:] and b[j:].
func lcsTable(a, b []string) [][]int {
	lcs := make([][]int, len(a)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
				continue
			}
			lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
		}
	}
	return lcs
}

func TestLineDiff_changedLine_showsBothSidesOnly(t *testing.T) {
	t.Parallel()
	got := lineDiff("a\nb\nc", "a\nB\nc\nd")
	want := "-   2 b\n+   2 B\n+   4 d\n"
	if got != want {
		t.Fatalf("lineDiff = %q, want %q", got, want)
	}
}

func TestLineDiff_equal_empty(t *testing.T) {
	t.Parallel()
	if got := lineDiff("a\nb", "a\nb"); got != "" {
		t.Fatalf("lineDiff of equal texts = %q, want empty", got)
	}
}
