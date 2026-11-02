package domain_test

import (
	"fmt"
	"reflect"
	"testing"
	"testing/quick"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

var metricsNoon = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

// metricsRun is a run of repo in state that, when terminal, took took.
func metricsRun(id, repo string, state domain.RunState, took time.Duration) domain.Run {
	run := domain.Run{ID: id, Tool: domain.ToolImplement, Repository: repo, State: state, StartedAt: metricsNoon}
	if state == domain.StateSucceeded || state == domain.StateFailed {
		finished := metricsNoon.Add(took)
		run.FinishedAt = &finished
	}
	return run
}

func failedRun(id string, reason domain.FailureReason) domain.Run {
	run := metricsRun(id, "acme/api", domain.StateFailed, time.Minute)
	run.Reason = reason
	return run
}

func onlyRepository(t *testing.T, stats []domain.RepositoryStats) domain.RepositoryStats {
	t.Helper()
	if len(stats) != 1 {
		t.Fatalf("Summarize = %d repositories, want 1: %+v", len(stats), stats)
	}
	return stats[0]
}

func TestSummarize_failedRuns_countedByReasonEmptyAsUnspecified(t *testing.T) {
	t.Parallel()
	runs := []domain.Run{
		failedRun("r1", domain.ReasonGateFailed),
		failedRun("r2", domain.ReasonGateFailed),
		failedRun("r3", domain.ReasonTokenCap),
		failedRun("r4", ""),
		failedRun("r5", "a reason no control plane gives"),
		// A reason on a run that did not fail is not a failure.
		func() domain.Run {
			r := metricsRun("r6", "acme/api", domain.StateSucceeded, time.Minute)
			r.Reason = domain.ReasonWallClock
			return r
		}(),
	}
	got := onlyRepository(t, domain.Summarize(runs)).Failures
	want := map[domain.FailureReason]int{
		domain.ReasonGateFailed:  2,
		domain.ReasonTokenCap:    1,
		domain.ReasonUnspecified: 2,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Failures = %v, want %v", got, want)
	}
}

func TestFailureReasons_fixedOrderEndingInUnspecified(t *testing.T) {
	t.Parallel()
	want := []domain.FailureReason{
		domain.ReasonGateFailed, domain.ReasonTokenCap, domain.ReasonWallClock,
		domain.ReasonDiffTooLarge, domain.ReasonNoCommits, domain.ReasonUnspecified,
	}
	got := domain.FailureReasons()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("FailureReasons = %v, want %v", got, want)
	}
	got[0] = "mutated"
	if domain.FailureReasons()[0] != domain.ReasonGateFailed {
		t.Fatal("FailureReasons hands out shared state")
	}
}

func TestSummarize_averageDuration_overFinishedRunsOnly(t *testing.T) {
	t.Parallel()
	runs := []domain.Run{
		metricsRun("r1", "acme/api", domain.StateSucceeded, 2*time.Minute),
		metricsRun("r2", "acme/api", domain.StateFailed, 4*time.Minute),
		metricsRun("r3", "acme/api", domain.StateRunning, 0),
		metricsRun("r4", "acme/api", domain.StateQueued, 0),
	}
	got := onlyRepository(t, domain.Summarize(runs))
	if got.Finished != 2 || got.WallTime != 6*time.Minute {
		t.Fatalf("Finished = %d, WallTime = %v; want 2 runs and 6m", got.Finished, got.WallTime)
	}
	if got.InFlight != 2 || got.Succeeded != 1 || got.Failed != 1 || got.Runs != 4 {
		t.Fatalf("Runs %d, Succeeded %d, Failed %d, InFlight %d; want 4, 1, 1, 2", got.Runs, got.Succeeded, got.Failed, got.InFlight)
	}
}

func TestSummarize_computeAndTokens_summedPerRepository(t *testing.T) {
	t.Parallel()
	a1 := metricsRun("a1", "acme/web", domain.StateSucceeded, time.Minute)
	a1.Compute, a1.Tokens = 90*time.Second, domain.TokenUsage{Input: 1, CacheCreation: 2, CacheRead: 4, Output: 8}
	b := metricsRun("b", "acme/api", domain.StateSucceeded, time.Minute)
	b.Compute, b.Tokens = 7*time.Second, domain.TokenUsage{Input: 1000}
	a2 := metricsRun("a2", "acme/web", domain.StateRunning, 0)
	a2.Compute, a2.Tokens = 30*time.Second, domain.TokenUsage{Input: 16, CacheCreation: 32, CacheRead: 64, Output: 128}

	got := domain.Summarize([]domain.Run{a1, b, a2})
	if len(got) != 2 || got[0].Repository != "acme/api" || got[1].Repository != "acme/web" {
		t.Fatalf("Summarize = %+v, want acme/api then acme/web", got)
	}
	if got[0].Compute != 7*time.Second || got[0].Tokens != b.Tokens {
		t.Fatalf("acme/api compute %v tokens %+v, want 7s and %+v", got[0].Compute, got[0].Tokens, b.Tokens)
	}
	wantTokens := domain.TokenUsage{Input: 17, CacheCreation: 34, CacheRead: 68, Output: 136}
	if got[1].Compute != 2*time.Minute || got[1].Tokens != wantTokens {
		t.Fatalf("acme/web compute %v tokens %+v, want 2m and %+v", got[1].Compute, got[1].Tokens, wantTokens)
	}
}

func TestSummarize_noRuns_emptyNotNil(t *testing.T) {
	t.Parallel()
	if got := domain.Summarize(nil); got == nil || len(got) != 0 {
		t.Fatalf("Summarize(nil) = %#v, want an empty slice", got)
	}
}

func deniedRun(id string, n int) domain.Run {
	run := metricsRun(id, "acme/api", domain.StateFailed, time.Minute)
	for i := range n {
		run.Egress.Record(domain.EgressDenial{At: metricsNoon.Add(time.Duration(i) * time.Second), Kind: domain.EgressConnect, Destination: fmt.Sprintf("%s-%d:443", id, i)})
	}
	return run
}

func TestSummarize_egressDenied_listedWithRunIDCappedCountFull(t *testing.T) {
	t.Parallel()
	first := deniedRun("first", 60)
	second := deniedRun("second", 70)
	// A run whose own list was cut still counts every attempt.
	third := deniedRun("third", domain.MaxEgressDenials+5)
	got := onlyRepository(t, domain.Summarize([]domain.Run{first, second, third}))

	if got.EgressDeniedCount != 60+70+domain.MaxEgressDenials+5 {
		t.Fatalf("EgressDeniedCount = %d, want %d", got.EgressDeniedCount, 60+70+domain.MaxEgressDenials+5)
	}
	if len(got.EgressDenied) != domain.MaxEgressDenials {
		t.Fatalf("EgressDenied lists %d, want the cap %d", len(got.EgressDenied), domain.MaxEgressDenials)
	}
	want0 := domain.RunDenial{RunID: "first", EgressDenial: first.Egress.Denied[0]}
	want60 := domain.RunDenial{RunID: "second", EgressDenial: second.Egress.Denied[0]}
	if got.EgressDenied[0] != want0 || got.EgressDenied[60] != want60 {
		t.Fatalf("EgressDenied[0] = %+v, [60] = %+v; want %+v and %+v in input order", got.EgressDenied[0], got.EgressDenied[60], want0, want60)
	}
}

func TestSummarize_egressDenied_underTheCapEveryEventListed(t *testing.T) {
	t.Parallel()
	got := onlyRepository(t, domain.Summarize([]domain.Run{deniedRun("a", 2), deniedRun("b", 1)}))
	if got.EgressDeniedCount != 3 || len(got.EgressDenied) != 3 || got.EgressDenied[2].RunID != "b" {
		t.Fatalf("count %d, listed %+v; want 3 events, the last of run b", got.EgressDeniedCount, got.EgressDenied)
	}
}

var quickStates = []domain.RunState{domain.StateQueued, domain.StateRunning, domain.StateSucceeded, domain.StateFailed}

var quickReasons = []domain.FailureReason{"", domain.ReasonGateFailed, domain.ReasonTokenCap, domain.ReasonWallClock, domain.ReasonDiffTooLarge, domain.ReasonNoCommits, "other"}

// Whatever the runs, every run lands in exactly one repository and one
// outcome, and every failed run in exactly one reason.
func TestSummarize_property_everyRunCountedOnce(t *testing.T) {
	t.Parallel()
	property := func(seeds []uint16) bool {
		runs := make([]domain.Run, 0, len(seeds))
		for i, s := range seeds {
			run := metricsRun(fmt.Sprint(i), fmt.Sprintf("acme/r%d", s%5), quickStates[int(s/5)%len(quickStates)], time.Minute)
			run.Reason = quickReasons[int(s/20)%len(quickReasons)]
			runs = append(runs, run)
		}
		total := 0
		for _, st := range domain.Summarize(runs) {
			failures := 0
			for _, n := range st.Failures {
				failures += n
			}
			if st.Succeeded+st.Failed+st.InFlight != st.Runs || failures != st.Failed {
				t.Logf("%s: runs %d, outcomes %d, failed %d, by reason %d", st.Repository, st.Runs, st.Succeeded+st.Failed+st.InFlight, st.Failed, failures)
				return false
			}
			total += st.Runs
		}
		return total == len(runs)
	}
	if err := quick.Check(property, &quick.Config{MaxCount: 300}); err != nil {
		t.Fatal(err)
	}
}
