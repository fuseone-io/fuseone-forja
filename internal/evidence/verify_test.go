package evidence_test

import (
	"strings"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/evidence"
)

func verifyRun() evidence.VerifyRun {
	return evidence.VerifyRun{
		RunID: "run-v1", Issue: 12,
		PullRequest: domain.PullRequest{Number: 57, URL: "https://github.com/fuseone-io/fuseone-forja/pull/57"},
		BaseBranch:  "main", BaseSHA: "a058ad9c0b1e2d3f4a5b6c7d8e9f0a1b2c3d4e5f",
		ContractSHA: "ea53aa91deb7ba2bb2695aa876dce4636adf9a97", HeadSHA: "9c1b2a3d4e5f60718293a4b5c6d7e8f901234567",
		QA: domain.QA{Verdict: domain.VerdictNotVerifiable, Reason: domain.NotVerifiableOnlyExcludedPaths, Excluded: []string{"README.md", "docs/x.md"}},
	}
}

func TestRenderVerify_sameRun_sameBody(t *testing.T) {
	t.Parallel()
	if a, b := evidence.RenderVerify(verifyRun()), evidence.RenderVerify(verifyRun()); a != b {
		t.Fatalf("two renders of one run differ:\n%s\n---\n%s", a, b)
	}
}

// The body pins what the verdict was reached on: the commits, the
// contract's blob, the pull request and the issue, and the verdict with
// its reason and every excluded path.
func TestRenderVerify_pinsShasVerdictAndExcludedPaths(t *testing.T) {
	t.Parallel()
	r := verifyRun()
	body := evidence.RenderVerify(r)
	for _, want := range []string{
		"Refs #12", "`run-v1`", "#57", r.PullRequest.URL, "`main`",
		"`" + r.BaseSHA + "`", "`" + r.ContractSHA + "`", "`" + r.HeadSHA + "`",
		"`not_verifiable`", "`only_excluded_paths`", "- `README.md`\n", "- `docs/x.md`\n",
		"forja.status run_id=run-v1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body does not contain %q:\n%s", want, body)
		}
	}
}

// Each identifier sits in its own row, so a body with two of them swapped
// is told apart.
func TestRenderVerify_eachShaInItsRow(t *testing.T) {
	t.Parallel()
	r := verifyRun()
	body := evidence.RenderVerify(r)
	for label, sha := range map[string]string{"Base commit": r.BaseSHA, "Head": r.HeadSHA, "Contract": r.ContractSHA} {
		if !rowHas(body, label, sha) {
			t.Errorf("no row %q carrying %s:\n%s", label, sha, body)
		}
	}
}

func rowHas(body, label, value string) bool {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "| "+label) && strings.Contains(line, value) {
			return true
		}
	}
	return false
}

// A path is the pull request's to choose: backticks and pipes in it stay
// inside its code span.
func TestRenderVerify_pathWithBackticks_cannotBreakOut(t *testing.T) {
	t.Parallel()
	r := verifyRun()
	r.QA.Excluded = []string{"docs/a`b|c.md"}
	body := evidence.RenderVerify(r)
	if !strings.Contains(body, "- ``docs/a`b\\|c.md``\n") {
		t.Fatalf("path not fenced as one code span:\n%s", body)
	}
}

func gate(name string, status domain.StepStatus, exit int) domain.GateResult {
	return domain.GateResult{Name: name, Command: "make " + name, Status: status, ExitCode: exit}
}

func redAt(name string) domain.GateReport {
	return domain.GateReport{Gates: []domain.GateResult{gate("build", domain.StepPassed, 0), gate(name, domain.StepFailed, 2)}, FailedStep: name}
}

// invertedRun is a verify run that ran its baseline green and judged two
// inversions, one that bit at test and one that escaped, after refusing a
// candidate that touched a test.
func invertedRun() evidence.VerifyRun {
	r := verifyRun()
	r.QA = domain.QA{Verdict: domain.VerdictDoesNotBite, Excluded: []string{}, Escaped: "forja-qa/3"}
	r.Baseline = domain.GateReport{Gates: []domain.GateResult{gate("build", domain.StepPassed, 0), gate("test", domain.StepPassed, 0)}}
	r.Tokens, r.Compute = domain.TokenUsage{Input: 1000, CacheRead: 230, Output: 400}, 90*time.Second
	r.Inversions = []domain.Inversion{
		{Branch: "forja-qa/1", Commit: "1111111111111111111111111111111111111111", Paths: []string{"internal/x_test.go"}, Rejected: domain.RejectTouchesNonProduction},
		{Branch: "forja-qa/2", Commit: "2222222222222222222222222222222222222222", Paths: []string{"internal/cost.go"},
			Patch: "-\treturn a / 60_000\n+\treturn a / 6_000\n", Admitted: true, Gates: redAt("test"), Outcome: domain.InversionBit},
		{Branch: "forja-qa/3", Commit: "3333333333333333333333333333333333333333", Paths: []string{"internal/a`b.go"},
			Patch: "```\n+escape\n", PatchTruncated: true, Admitted: true, Gates: domain.GateReport{Gates: []domain.GateResult{gate("build", domain.StepPassed, 0)}}, Outcome: domain.InversionEscaped},
	}
	return r
}

// The body shows the baseline, each inversion with its branch, commit,
// outcome, red gate, paths and patch, and this run's cost.
func TestRenderVerify_inversions_eachWithOutcomeRedGatePathsAndPatch(t *testing.T) {
	t.Parallel()
	body := evidence.RenderVerify(invertedRun())
	inOrder(t, body,
		"| Baseline gates on the head | passed |",
		"| Tokens in / out | 1230 / 400 |", "| Compute minutes | 1.500 |",
		"## Inversions",
		"### `forja-qa/1`", "`1111111111111111111111111111111111111111`", "| Outcome | rejected: `touches_non_production` |", "- `internal/x_test.go`",
		"### `forja-qa/2`", "| Outcome | `bit` |", "| Red gate | `test` |", "- `internal/cost.go`", "```\n-\treturn a / 60_000\n+\treturn a / 6_000\n```\n",
		"### `forja-qa/3`", "| Outcome | `escaped` |", "| Red gate | — |", "- ``internal/a`b.go``", "_The patch was cut", "````\n```\n+escape\n````\n",
	)
}

// A baseline that went red names the step; one that never ran says so,
// as does a run with no inversion.
func TestRenderVerify_baselineRedOrNotRun_noInversions(t *testing.T) {
	t.Parallel()
	r := verifyRun()
	r.Baseline = redAt("vet")
	if body := evidence.RenderVerify(r); !strings.Contains(body, "| Baseline gates on the head | failed at `vet` |") {
		t.Fatalf("red baseline not named:\n%s", body)
	}
	body := evidence.RenderVerify(verifyRun())
	inOrder(t, body, "| Baseline gates on the head | — |", "## Inversions", "_None._")
}

func inOrder(t *testing.T, s string, parts ...string) {
	t.Helper()
	from := 0
	for _, p := range parts {
		i := strings.Index(s[from:], p)
		if i < 0 {
			t.Fatalf("body lacks %q after offset %d:\n%s", p, from, s)
		}
		from += i + len(p)
	}
}

// A run with no verdict says so, and why; a run with no excluded path
// says none.
func TestRenderVerify_noVerdictNoExcluded_saysSo(t *testing.T) {
	t.Parallel()
	r := verifyRun()
	r.QA = domain.QA{Excluded: []string{}}
	r.Reason = domain.ReasonInversionUnavailable
	body := evidence.RenderVerify(r)
	for _, want := range []string{"| Verdict | — |", "`inversion_unavailable`", "_None._"} {
		if !strings.Contains(body, want) {
			t.Errorf("body does not contain %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "not_verifiable") {
		t.Errorf("body names a verdict the run did not reach:\n%s", body)
	}
}
