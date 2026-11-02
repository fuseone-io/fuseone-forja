package contract_test

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/contract"
	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

const minimal = `version: 1
image: golang:1.25
gates:
  - name: build
    run: make build
`

func mustParse(t *testing.T, src string) domain.Contract {
	t.Helper()
	c, err := contract.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return c
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return src
}

// fieldErrors extracts the field errors from a Parse error, failing when the
// error is not the Errors type callers are promised.
func fieldErrors(t *testing.T, err error) contract.Errors {
	t.Helper()
	if err == nil {
		t.Fatal("Parse returned nil error, want field errors")
	}
	var errs contract.Errors
	if !errors.As(err, &errs) {
		t.Fatalf("Parse error %v (%T) is not contract.Errors", err, err)
	}
	return errs
}

func TestParse_prdExample_acceptedAndNormalised(t *testing.T) {
	t.Parallel()
	c := mustParse(t, string(mustReadFile(t, "testdata/prd-example.yaml")))

	wantGates := []domain.Gate{
		{Name: "build", Run: "make build", Needs: []string{}},
		{Name: "lint", Run: "make lint", Needs: []string{}},
		{Name: "test", Run: "make test", Needs: []string{}},
		{Name: "integration", Run: "make integration-test", Needs: []string{"postgres"}},
	}
	if !reflect.DeepEqual(c.Gates, wantGates) {
		t.Errorf("Gates = %+v, want %+v", c.Gates, wantGates)
	}
	if c.Version != 1 || c.Image != "golang:1.24" {
		t.Errorf("Version = %d, Image = %q, want 1 and golang:1.24", c.Version, c.Image)
	}
	if got := c.Services["postgres"].Env["POSTGRES_PASSWORD"]; got != "test" {
		t.Errorf("services.postgres.env.POSTGRES_PASSWORD = %q, want test", got)
	}
	if got := c.Services["postgres"].Image; got != "postgres:17" {
		t.Errorf("services.postgres.image = %q, want postgres:17", got)
	}
	if c.Limits.WallClock != 30*time.Minute || c.Limits.MaxDiffLines != 400 {
		t.Errorf("Limits = %+v, want 30m and 400", c.Limits)
	}
	lists := []struct {
		name string
		got  []string
		want []string
	}{
		{"setup", c.Setup, []string{"make deps"}},
		{"registries", c.Registries, []string{"ghcr.io/my-org"}},
		{"verify.exclude", c.Verify.Exclude, []string{"**/*.md", "migrations/**"}},
		{"proof_paths", c.ProofPaths, []string{".forja.yaml", "Makefile", "scripts/**", "**/*_test.go"}},
		{"human_merge_paths", c.HumanMergePaths, []string{".github/**"}},
		{"agent_context", c.AgentContext, []string{"CLAUDE.md", "CONTRIBUTING.md"}},
	}
	for _, l := range lists {
		if !reflect.DeepEqual(l.got, l.want) {
			t.Errorf("%s = %q, want %q", l.name, l.got, l.want)
		}
	}
}

func TestParse_pilotContract_accepted(t *testing.T) {
	t.Parallel()
	c := mustParse(t, string(mustReadFile(t, "../../.forja.yaml")))

	wantServices := map[string]domain.Service{
		"postgres": {Image: "postgres:16", Env: map[string]string{"POSTGRES_HOST_AUTH_METHOD": "trust"}},
	}
	if !reflect.DeepEqual(c.Services, wantServices) {
		t.Errorf("Services = %#v, want %#v", c.Services, wantServices)
	}
	if c.Registries == nil || len(c.Registries) != 0 {
		t.Errorf("Registries = %#v, want empty and non-nil", c.Registries)
	}
	var names []string
	for _, g := range c.Gates {
		names = append(names, g.Name)
	}
	want := []string{"build", "vet", "lint", "test", "race", "integration"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("gate names = %q, want %q", names, want)
	}
	if needs := c.Gates[len(c.Gates)-1].Needs; !reflect.DeepEqual(needs, []string{"postgres"}) {
		t.Errorf("integration needs = %q, want [postgres]", needs)
	}
}

// The pilot's gates are `make <target>` with no -f, and GNU make reads
// GNUmakefile, then makefile, before Makefile. An agent that adds either one
// replaces every gate without touching an existing proof_paths file, so the
// pilot contract must send that addition to a human merge.
func TestParse_pilotContract_addedMakefileOverrideMarksHumanMerge(t *testing.T) {
	t.Parallel()
	c := mustParse(t, string(mustReadFile(t, "../../.forja.yaml")))

	for _, name := range []string{"GNUmakefile", "makefile"} {
		added := domain.FileChange{Path: name, Kind: domain.ChangeAdded, Added: 6}
		got := domain.ClassifyProof(c, []domain.FileChange{added})
		want := domain.Proof{HumanMergePaths: []domain.FileChange{added}}
		if !reflect.DeepEqual(got, want) || !got.HumanMerge() {
			t.Errorf("added %s: ClassifyProof = %+v (human merge %v), want %+v", name, got, got.HumanMerge(), want)
		}
	}
}

func TestParse_omittedSections_emptyNotNil(t *testing.T) {
	t.Parallel()
	c := mustParse(t, minimal)

	lists := map[string][]string{
		"setup":             c.Setup,
		"registries":        c.Registries,
		"verify.exclude":    c.Verify.Exclude,
		"proof_paths":       c.ProofPaths,
		"human_merge_paths": c.HumanMergePaths,
		"agent_context":     c.AgentContext,
		"gates[0].needs":    c.Gates[0].Needs,
	}
	for name, got := range lists {
		if got == nil || len(got) != 0 {
			t.Errorf("%s = %#v, want empty and non-nil", name, got)
		}
	}
	if c.Services == nil || len(c.Services) != 0 {
		t.Errorf("services = %#v, want empty and non-nil", c.Services)
	}
	if c.Limits != (domain.Limits{}) {
		t.Errorf("limits = %+v, want zero", c.Limits)
	}
}

// Each input is written so that the offending key or value sits on an
// unambiguous line; the expected line is that one.
func TestParse_invalid_errorNamesPathAndLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
		path string
		line int
	}{
		{name: "version 2", path: "version", line: 1, src: "version: 2\nimage: golang:1.25\ngates:\n  - name: build\n    run: make build\n"},
		{name: "version absent", path: "version", line: 1, src: "image: golang:1.25\ngates:\n  - name: build\n    run: make build\n"},
		{name: "gate without name", path: "gates[0].name", line: 4, src: "version: 1\nimage: golang:1.25\ngates:\n  - run: make build\n"},
		{name: "gate without run", path: "gates[0].run", line: 4, src: "version: 1\nimage: golang:1.25\ngates:\n  - name: build\n"},
		{name: "needs unknown service", path: "gates[0].needs[0]", line: 6, src: minimal + "    needs: [redis]\n"},
		{name: "bad glob in verify.exclude", path: "verify.exclude[0]", line: 8, src: minimal + "verify:\n  exclude:\n    - \"[abc\"\n"},
		{name: "bad glob in proof_paths", path: "proof_paths[1]", line: 8, src: minimal + "proof_paths:\n  - Makefile\n  - \"[abc\"\n"},
		{name: "bad glob in human_merge_paths", path: "human_merge_paths[0]", line: 7, src: minimal + "human_merge_paths:\n  - \"[abc\"\n"},
		{name: "wall_clock not a duration", path: "limits.wall_clock", line: 7, src: minimal + "limits:\n  wall_clock: thirty\n"},
		{name: "wall_clock negative", path: "limits.wall_clock", line: 7, src: minimal + "limits:\n  wall_clock: -1m\n"},
		{name: "wall_clock zero", path: "limits.wall_clock", line: 7, src: minimal + "limits:\n  wall_clock: 0s\n"},
		{name: "max_diff_lines not an integer", path: "limits.max_diff_lines", line: 7, src: minimal + "limits:\n  max_diff_lines: many\n"},
		{name: "max_diff_lines zero", path: "limits.max_diff_lines", line: 7, src: minimal + "limits:\n  max_diff_lines: 0\n"},
		{name: "unknown top-level key", path: "foo", line: 3, src: "version: 1\nimage: golang:1.25\nfoo: bar\ngates:\n  - name: build\n    run: make build\n"},
		{name: "unknown key in gate", path: "gates[0].foo", line: 6, src: minimal + "    foo: bar\n"},
		{name: "unknown key in limits", path: "limits.foo", line: 7, src: minimal + "limits:\n  foo: 1\n"},
		{name: "unknown key in service", path: "services.postgres.foo", line: 6, src: "version: 1\nimage: golang:1.25\nservices:\n  postgres:\n    image: postgres:17\n    foo: bar\ngates:\n  - name: build\n    run: make build\n"},
		{name: "gates of the wrong kind", path: "gates", line: 3, src: "version: 1\nimage: golang:1.25\ngates: make test\n"},
		{name: "image absent", path: "image", line: 1, src: "version: 1\ngates:\n  - name: build\n    run: make build\n"},
		{name: "image empty", path: "image", line: 2, src: "version: 1\nimage: \"\"\ngates:\n  - name: build\n    run: make build\n"},
		{name: "setup entry empty", path: "setup[0]", line: 4, src: "version: 1\nimage: golang:1.25\nsetup:\n  - \"\"\ngates:\n  - name: build\n    run: make build\n"},
		{name: "registries entry of the wrong kind", path: "registries[0]", line: 4, src: "version: 1\nimage: golang:1.25\nregistries:\n  - [a]\ngates:\n  - name: build\n    run: make build\n"},
		{name: "service without image", path: "services.postgres.image", line: 4, src: "version: 1\nimage: golang:1.25\nservices:\n  postgres:\n    env: {A: b}\ngates:\n  - name: build\n    run: make build\n"},
		{name: "service env of the wrong kind", path: "services.postgres.env", line: 6, src: "version: 1\nimage: golang:1.25\nservices:\n  postgres:\n    image: postgres:17\n    env: [a]\ngates:\n  - name: build\n    run: make build\n"},
		{name: "not a mapping", path: "", line: 1, src: "- a\n- b\n"},
		{name: "duplicate top-level key", path: "image", line: 3, src: "version: 1\nimage: golang:1.25\nimage: golang:1.24\ngates:\n  - name: build\n    run: make build\n"},
		{name: "duplicate service", path: "services.postgres", line: 6, src: "version: 1\nimage: golang:1.25\nservices:\n  postgres:\n    image: postgres:17\n  postgres:\n    image: postgres:16\ngates:\n  - name: build\n    run: make build\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := contract.Parse([]byte(tt.src))
			errs := fieldErrors(t, err)
			if len(errs) != 1 {
				t.Fatalf("got %d errors %v, want exactly one", len(errs), errs)
			}
			if errs[0].Path != tt.path || errs[0].Line != tt.line {
				t.Fatalf("error = %q (line %d), want path %q at line %d", errs[0].Path, errs[0].Line, tt.path, tt.line)
			}
			if errs[0].Message == "" {
				t.Fatal("error has no message")
			}
			if !strings.Contains(errs[0].Error(), tt.path) || !strings.Contains(err.Error(), tt.path) {
				t.Fatalf("Error() %q / %q does not carry the path %q", errs[0].Error(), err.Error(), tt.path)
			}
		})
	}
}

// A registry entry outside host[:port][/path] is a contract defect at its
// own index and line; an uppercase one is the same entry, lowercased.
func TestParse_registryEntryMalformed_errorWithPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		entry string
		// want is the normalised entry when the contract is accepted.
		want string
	}{
		{name: "scheme", entry: "https://ghcr.io/my-org"},
		{name: "query", entry: "ghcr.io/my-org?x=1"},
		{name: "wildcard", entry: "'*.github.com'"},
		{name: "dot dot segment", entry: "ghcr.io/my-org/../other"},
		{name: "trailing slash", entry: "ghcr.io/my-org/"},
		{name: "uppercase normalised", entry: "GHCR.io/My-Org", want: "ghcr.io/my-org"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			src := minimal + "registries:\n  - npm.pkg.github.com\n  - " + tt.entry + "\n"
			c, err := contract.Parse([]byte(src))
			if tt.want != "" {
				if err != nil || !reflect.DeepEqual(c.Registries, []string{"npm.pkg.github.com", tt.want}) {
					t.Fatalf("Parse = (%q, %v), want [npm.pkg.github.com %s]", c.Registries, err, tt.want)
				}
				return
			}
			errs := fieldErrors(t, err)
			if len(errs) != 1 || errs[0].Path != "registries[1]" || errs[0].Line != 8 || errs[0].Message == "" {
				t.Fatalf("errors = %v, want one at registries[1] on line 8", errs)
			}
		})
	}
}

func TestParse_severalErrors_allReported(t *testing.T) {
	t.Parallel()
	// The needs reference can only be judged once services are known, so it
	// is the error most likely to be reported out of document order.
	src := "version: 2\nimage: golang:1.25\ngates:\n  - name: build\n    run: make build\n    needs: [redis]\nfoo: bar\n"

	_, err := contract.Parse([]byte(src))
	errs := fieldErrors(t, err)
	var got []string
	var lines []int
	for _, e := range errs {
		got = append(got, e.Path)
		lines = append(lines, e.Line)
	}
	want := []string{"version", "gates[0].needs[0]", "foo"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("error paths = %q, want %q in document order", got, want)
	}
	if !reflect.DeepEqual(lines, []int{1, 6, 7}) {
		t.Fatalf("error lines = %v, want [1 6 7]", lines)
	}
}

func TestParse_doubleStarGlob_accepted(t *testing.T) {
	t.Parallel()
	src := minimal +
		"verify:\n  exclude: [\"**/*.md\"]\n" +
		"proof_paths: [\"scripts/**\", \"**/testdata/**\"]\n" +
		"human_merge_paths: [\"**/*.md\", \"scripts/**\", \"**/testdata/**\"]\n"

	c := mustParse(t, src)
	if !reflect.DeepEqual(c.HumanMergePaths, []string{"**/*.md", "scripts/**", "**/testdata/**"}) {
		t.Fatalf("human_merge_paths = %q, want the three double-star globs kept verbatim", c.HumanMergePaths)
	}
}

func TestParse_duplicateGateName_rejected(t *testing.T) {
	t.Parallel()
	src := minimal + "  - name: build\n    run: make build\n"

	_, err := contract.Parse([]byte(src))
	errs := fieldErrors(t, err)
	if len(errs) != 1 || errs[0].Path != "gates[1].name" || errs[0].Line != 6 {
		t.Fatalf("errors = %v, want one at gates[1].name line 6", errs)
	}
	if !strings.Contains(errs[0].Message, "duplicate") {
		t.Fatalf("message %q does not say duplicate", errs[0].Message)
	}
}

func TestParse_malformedYAML_errorsNotPanic(t *testing.T) {
	t.Parallel()
	_, err := contract.Parse([]byte("version: 1\nimage: [unterminated\n"))
	errs := fieldErrors(t, err)
	if len(errs) != 1 || errs[0].Message == "" {
		t.Fatalf("errors = %v, want one with a message", errs)
	}
}
