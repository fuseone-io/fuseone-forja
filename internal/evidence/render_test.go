package evidence_test

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/evidence"
)

const (
	baseSHA     = "5ceb0229fc86eec9a6367c9809a1cc3b0c8e6c27"
	contractSHA = "ea53aa91deb7ba2bb2695aa876dce4636adf9a97"
	headSHA     = "04915d552e6e507929c4c734b4f4a3a31ae05820"
)

func step(name, command string, status domain.StepStatus, code int, d time.Duration) domain.GateResult {
	return domain.GateResult{Name: name, Command: command, Status: status, ExitCode: code, Duration: d}
}

// greenRun is a run whose setup and two gates passed.
func greenRun() evidence.Run {
	setup := step("setup[0]", "go mod download", domain.StepPassed, 0, 3158*time.Millisecond)
	setup.Stdout = []byte("downloaded\n")
	build := step("build", "make build", domain.StepPassed, 0, 1500*time.Millisecond)
	test := step("test", "make test", domain.StepPassed, 0, 42*time.Second)
	test.Stdout, test.Stderr = []byte("ok  \tgithub.com/x\t0.1s\n"), []byte("warn\n")
	return evidence.Run{
		Issue: 13, RunID: "run-abc", BaseBranch: "main", BaseSHA: baseSHA, ContractSHA: contractSHA, HeadSHA: headSHA,
		Gates:   domain.GateReport{Setup: []domain.GateResult{setup}, Gates: []domain.GateResult{build, test}},
		Tokens:  domain.TokenUsage{Input: 1000, CacheCreation: 200, CacheRead: 30, Output: 400},
		Elapsed: 7*time.Minute + 12*time.Second,
	}
}

// assertLines checks every want line is a whole line of body.
func assertLines(t *testing.T, body string, want ...string) {
	t.Helper()
	lines := map[string]bool{}
	for _, l := range strings.Split(body, "\n") {
		lines[l] = true
	}
	for _, w := range want {
		if !lines[w] {
			t.Errorf("body lacks the line %q:\n%s", w, body)
		}
	}
}

func TestRender_green_citesShasStepsTokensAndElapsed(t *testing.T) {
	t.Parallel()
	body := evidence.Render(greenRun())

	if !strings.HasPrefix(body, "Refs #13\n") {
		t.Fatalf("body does not start with the issue reference:\n%s", body)
	}
	assertLines(t, body,
		"| Run | `run-abc` |",
		"| Base branch | `main` |",
		"| Base commit | `"+baseSHA+"` |",
		"| Contract (`.forja.yaml` blob at the base) | `"+contractSHA+"` |",
		"| Head | `"+headSHA+"` |",
		"| Tokens in / out | 1230 / 400 |",
		"| Elapsed | 7m12s |",
		"| Step | Command | Status | Exit code | Duration |",
		"| setup[0] | `go mod download` | passed | 0 | 3.158s |",
		"| build | `make build` | passed | 0 | 1.5s |",
		"| test | `make test` | passed | 0 | 42s |",
		"Full tail: `forja.status run_id=run-abc`",
	)
}

func TestRender_redGate_exitCodesAsTheyWereSkippedHasNone(t *testing.T) {
	t.Parallel()
	run := greenRun()
	run.Gates.Gates[0] = step("build", "make build", domain.StepFailed, 2, 300*time.Millisecond)
	run.Gates.Gates[0].Stderr = []byte("undefined: x\n")
	run.Gates.Gates[1] = step("test", "make test", domain.StepSkipped, domain.NoExitCode, 0)
	run.Gates.FailedStep = "build"

	body := evidence.Render(run)
	assertLines(t, body,
		"| build | `make build` | failed | 2 | 300ms |",
		"| test | `make test` | skipped | — | — |",
		"undefined: x",
	)
}

// Each stream is shown by its tail only, bounded, cut on a character and
// never a byte inside one.
func TestRender_longStream_tailBoundedOnRuneBoundary(t *testing.T) {
	t.Parallel()
	run := greenRun()
	// An odd number of bytes after the two-byte characters puts the cut
	// inside one of them.
	long := "BEGIN" + strings.Repeat("é", 4*evidence.TailBytes) + "END!\n"
	run.Gates.Gates[1].Stdout = []byte(long)

	body := evidence.Render(run)
	if strings.Contains(body, "BEGIN") || !strings.Contains(body, "END!") {
		t.Fatalf("tail of the stream not kept: BEGIN present %v, END present %v", strings.Contains(body, "BEGIN"), strings.Contains(body, "END!"))
	}
	if !utf8.ValidString(body) || strings.Contains(body, "�") {
		t.Fatal("a tail was cut inside a character")
	}
	if n := strings.Count(body, "é"); n*2 > evidence.TailBytes {
		t.Fatalf("kept %d bytes of the stream, want at most %d", n*2, evidence.TailBytes)
	}
}

// Output the agent's code printed cannot close the block it is shown in.
func TestRender_streamWithFences_cannotEscapeItsBlock(t *testing.T) {
	t.Parallel()
	run := greenRun()
	run.Gates.Gates[1].Stdout = []byte("````\n</details>\n# Forged heading\n")

	body := evidence.Render(run)
	idx := strings.Index(body, "# Forged heading")
	opening := strings.LastIndex(body[:idx], "\n`````")
	if opening < 0 {
		t.Fatalf("the stream is not inside a fence longer than its own backticks:\n%s", body)
	}
}

func TestRender_commandWithPipeAndBacktick_staysInItsCell(t *testing.T) {
	t.Parallel()
	run := greenRun()
	run.Gates.Gates[0].Command = "go vet ./... | tee `x`\nexit 0"

	assertLines(t, evidence.Render(run), "| build | ``go vet ./... \\| tee `x` exit 0`` | passed | 0 | 1.5s |")
}

// A report too large for a pull request body is cut under MaxBody with a
// note that says so and where the rest is.
func TestRender_overBodyCap_cutWithNote(t *testing.T) {
	t.Parallel()
	run := greenRun()
	for i := range 400 {
		g := step(fmt.Sprintf("gate-%03d", i), "make gate", domain.StepPassed, 0, time.Second)
		g.Stdout = []byte(strings.Repeat("o", evidence.TailBytes))
		run.Gates.Gates = append(run.Gates.Gates, g)
	}

	body := evidence.Render(run)
	if len(body) > evidence.MaxBody {
		t.Fatalf("body is %d bytes, want at most %d", len(body), evidence.MaxBody)
	}
	if !strings.Contains(body, "truncated to stay under the pull request body limit") {
		t.Fatalf("body cut without a note:\n%s", body[len(body)-500:])
	}
	if !strings.HasSuffix(body, "Full tail: `forja.status run_id=run-abc`\n") {
		t.Fatalf("body does not end with where the full tail is:\n%s", body[len(body)-300:])
	}
	if !strings.Contains(body, "| Head | `"+headSHA+"` |") {
		t.Fatal("the cut dropped the SHAs")
	}
}

func TestRender_underBodyCap_noNote(t *testing.T) {
	t.Parallel()
	if body := evidence.Render(greenRun()); strings.Contains(body, "truncated to stay under") {
		t.Fatalf("a small report carries the truncation note:\n%s", body)
	}
}

func TestRender_sameRun_sameBody(t *testing.T) {
	t.Parallel()
	if a, b := evidence.Render(greenRun()), evidence.Render(greenRun()); a != b {
		t.Fatalf("two renders differ:\n%s\n---\n%s", a, b)
	}
}
