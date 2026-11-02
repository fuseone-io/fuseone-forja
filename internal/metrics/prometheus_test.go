package metrics_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/metrics"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

var noon = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

// weird is a repository name with every character a label value escapes.
const weird = "we\"ird\\re\npo"

func run(id, repo string, state domain.RunState, took time.Duration) domain.Run {
	r := domain.Run{ID: id, Tool: domain.ToolImplement, Repository: repo, State: state, StartedAt: noon}
	if state == domain.StateSucceeded || state == domain.StateFailed {
		finished := noon.Add(took)
		r.FinishedAt = &finished
	}
	return r
}

func fixtureStore(t *testing.T) *runstore.Memory {
	t.Helper()
	ok := run("api-ok", "acme/api", domain.StateSucceeded, time.Minute)
	ok.Compute, ok.Tokens = 20*time.Second, domain.TokenUsage{Input: 1, CacheRead: 3, Output: 2}
	gate := run("api-gate", "acme/api", domain.StateFailed, 2*time.Minute)
	gate.Compute, gate.Reason = 40*time.Second, domain.ReasonGateFailed
	gate.Egress = domain.EgressReport{Denied: []domain.EgressDenial{{At: noon, Kind: domain.EgressConnect, Destination: "example.com:443"}}, Count: 3}
	odd := run("odd", weird, domain.StateRunning, 0)
	odd.Tokens = domain.TokenUsage{Output: 5}
	odd.Egress = domain.EgressReport{Count: 1}

	store := runstore.NewMemory()
	for _, r := range []domain.Run{ok, gate, run("api-queued", "acme/api", domain.StateQueued, 0), odd} {
		if err := store.Put(context.Background(), r); err != nil {
			t.Fatalf("Put %s: %v", r.ID, err)
		}
	}
	return store
}

var prices = domain.PriceTable{Input: 2_000_000, CacheRead: 200_000, Output: 10_000_000, ComputeMinute: 40_000}

func scrape(t *testing.T, h http.Handler) (*http.Response, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil))
	res := rec.Result()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return res, string(body)
}

const goldenWithoutCost = `# HELP forja_runs Runs by repository and state; in_flight is queued and running.
# TYPE forja_runs gauge
forja_runs{repository="acme/api",state="succeeded"} 1
forja_runs{repository="acme/api",state="failed"} 1
forja_runs{repository="acme/api",state="in_flight"} 1
forja_runs{repository="we\"ird\\re\npo",state="succeeded"} 0
forja_runs{repository="we\"ird\\re\npo",state="failed"} 0
forja_runs{repository="we\"ird\\re\npo",state="in_flight"} 1
# HELP forja_run_failures Failed runs by repository and reason.
# TYPE forja_run_failures gauge
forja_run_failures{repository="acme/api",reason="gate_failed"} 1
forja_run_failures{repository="acme/api",reason="token_cap"} 0
forja_run_failures{repository="acme/api",reason="wall_clock"} 0
forja_run_failures{repository="acme/api",reason="diff_too_large"} 0
forja_run_failures{repository="acme/api",reason="no_commits"} 0
forja_run_failures{repository="acme/api",reason="unspecified"} 0
forja_run_failures{repository="we\"ird\\re\npo",reason="gate_failed"} 0
forja_run_failures{repository="we\"ird\\re\npo",reason="token_cap"} 0
forja_run_failures{repository="we\"ird\\re\npo",reason="wall_clock"} 0
forja_run_failures{repository="we\"ird\\re\npo",reason="diff_too_large"} 0
forja_run_failures{repository="we\"ird\\re\npo",reason="no_commits"} 0
forja_run_failures{repository="we\"ird\\re\npo",reason="unspecified"} 0
# HELP forja_run_duration_seconds_sum Wall time of the finished runs by repository.
# TYPE forja_run_duration_seconds_sum gauge
forja_run_duration_seconds_sum{repository="acme/api"} 180
forja_run_duration_seconds_sum{repository="we\"ird\\re\npo"} 0
# HELP forja_run_duration_seconds_count Finished runs by repository.
# TYPE forja_run_duration_seconds_count gauge
forja_run_duration_seconds_count{repository="acme/api"} 2
forja_run_duration_seconds_count{repository="we\"ird\\re\npo"} 0
# HELP forja_compute_seconds Container time of the runs' workspaces and sidecars by repository.
# TYPE forja_compute_seconds gauge
forja_compute_seconds{repository="acme/api"} 60
forja_compute_seconds{repository="we\"ird\\re\npo"} 0
# HELP forja_tokens Model tokens metered by repository and direction; in counts both cache fields.
# TYPE forja_tokens gauge
forja_tokens{repository="acme/api",direction="in"} 4
forja_tokens{repository="acme/api",direction="out"} 2
forja_tokens{repository="we\"ird\\re\npo",direction="in"} 0
forja_tokens{repository="we\"ird\\re\npo",direction="out"} 5
# HELP forja_egress_denied Egress attempts refused by repository.
# TYPE forja_egress_denied gauge
forja_egress_denied{repository="acme/api"} 3
forja_egress_denied{repository="we\"ird\\re\npo"} 1
`

// The cost of api-ok is 22 micros of tokens and 13 333 of compute, of
// api-gate 26 666 of compute, and of odd 50 of tokens: each run floored on
// its own, then summed.
const goldenCost = `# HELP forja_estimated_cost_usd_micros Sum of the runs' estimated cost at the server's price table, in USD micros, by repository.
# TYPE forja_estimated_cost_usd_micros gauge
forja_estimated_cost_usd_micros{repository="acme/api"} 40021
forja_estimated_cost_usd_micros{repository="we\"ird\\re\npo"} 50
`

func TestHandler_fixtureStore_exactExposition(t *testing.T) {
	t.Parallel()
	p := prices
	res, body := scrape(t, metrics.Handler(fixtureStore(t), &p))

	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if got := res.Header.Get("Content-Type"); got != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want the text format 0.0.4", got)
	}
	if want := goldenWithoutCost + goldenCost; body != want {
		t.Fatalf("exposition =\n%s\nwant\n%s", body, want)
	}
}

func TestHandler_noPrices_noCostMetric(t *testing.T) {
	t.Parallel()
	_, body := scrape(t, metrics.Handler(fixtureStore(t), nil))

	if body != goldenWithoutCost {
		t.Fatalf("exposition =\n%s\nwant\n%s", body, goldenWithoutCost)
	}
	if strings.Contains(body, "forja_estimated_cost_usd_micros") {
		t.Fatal("a cost metric with no price table")
	}
}

func TestHandler_emptyStore_familiesWithNoSamples(t *testing.T) {
	t.Parallel()
	_, body := scrape(t, metrics.Handler(runstore.NewMemory(), nil))

	for line := range strings.SplitSeq(strings.TrimSuffix(body, "\n"), "\n") {
		if !strings.HasPrefix(line, "# ") {
			t.Fatalf("sample %q from an empty store", line)
		}
	}
	if strings.Count(body, "# TYPE ") != 7 {
		t.Fatalf("exposition declares %d families, want 7:\n%s", strings.Count(body, "# TYPE "), body)
	}
}

type failingSource struct{}

func (failingSource) List(context.Context, domain.RunFilter) ([]domain.Run, error) {
	return nil, errors.New("store unavailable")
}

func TestHandler_listFails_500AndNoPartialExposition(t *testing.T) {
	t.Parallel()
	res, body := scrape(t, metrics.Handler(failingSource{}, nil))

	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", res.StatusCode)
	}
	if strings.Contains(body, "forja_") {
		t.Fatalf("body %q carries metrics from a failed read", body)
	}
}
