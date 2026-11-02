// Package metrics exposes the runs the control plane recorded as
// Prometheus metrics for the platform owner's panel. An exposition format
// is neither a tool of the MCP server nor a concern of the run store, so it
// has a package of its own, which reads the store through the interface it
// declares.
package metrics

import (
	"bytes"
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// ContentType is the Prometheus text exposition format, version 0.0.4.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

// Source is what the exposition reads: every run a filter matches.
type Source interface {
	List(ctx context.Context, f domain.RunFilter) ([]domain.Run, error)
}

// Handler serves every run of src, folded by repository at each scrape, as
// gauges in the text format. The cost metric is absent when prices is nil.
// A failed read answers 500 and no metric.
func Handler(src Source, prices *domain.PriceTable) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runs, err := src.List(r.Context(), domain.RunFilter{})
		if err != nil {
			http.Error(w, "forja: the run store could not be read", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", ContentType)
		// A scraper that went away has nothing to be told.
		_, _ = w.Write(exposition(runs, prices))
	})
}

// sample is one line of a family: its labels, as name and value pairs in
// order, and its value already formatted.
type sample struct {
	labels []string
	value  string
}

// family is one metric with its help text and samples.
type family struct {
	name, help string
	samples    []sample
}

func exposition(runs []domain.Run, prices *domain.PriceTable) []byte {
	stats := domain.Summarize(runs)
	families := []family{
		runsFamily(stats), failuresFamily(stats),
		perRepository("forja_run_duration_seconds_sum", "Wall time of the finished runs by repository.", stats,
			func(st domain.RepositoryStats) string { return seconds(st.WallTime.Seconds()) }),
		perRepository("forja_run_duration_seconds_count", "Finished runs by repository.", stats,
			func(st domain.RepositoryStats) string { return strconv.Itoa(st.Finished) }),
		perRepository("forja_compute_seconds", "Container time of the runs' workspaces and sidecars by repository.", stats,
			func(st domain.RepositoryStats) string { return seconds(st.Compute.Seconds()) }),
		tokensFamily(stats),
		perRepository("forja_egress_denied", "Egress attempts refused by repository.", stats,
			func(st domain.RepositoryStats) string { return strconv.Itoa(st.EgressDeniedCount) }),
	}
	if prices != nil {
		families = append(families, costFamily(runs, stats, *prices))
	}
	var b bytes.Buffer
	for _, f := range families {
		write(&b, f)
	}
	return b.Bytes()
}

func runsFamily(stats []domain.RepositoryStats) family {
	f := family{name: "forja_runs", help: "Runs by repository and state; in_flight is queued and running."}
	for _, st := range stats {
		for _, s := range []struct {
			state string
			n     int
		}{{"succeeded", st.Succeeded}, {"failed", st.Failed}, {"in_flight", st.InFlight}} {
			f.samples = append(f.samples, sample{[]string{"repository", st.Repository, "state", s.state}, strconv.Itoa(s.n)})
		}
	}
	return f
}

func failuresFamily(stats []domain.RepositoryStats) family {
	f := family{name: "forja_run_failures", help: "Failed runs by repository and reason."}
	for _, st := range stats {
		for _, reason := range domain.FailureReasons() {
			f.samples = append(f.samples, sample{[]string{"repository", st.Repository, "reason", string(reason)}, strconv.Itoa(st.Failures[reason])})
		}
	}
	return f
}

func tokensFamily(stats []domain.RepositoryStats) family {
	f := family{name: "forja_tokens", help: "Model tokens metered by repository and direction; in counts both cache fields."}
	for _, st := range stats {
		f.samples = append(f.samples,
			sample{[]string{"repository", st.Repository, "direction", "in"}, strconv.FormatInt(st.Tokens.In(), 10)},
			sample{[]string{"repository", st.Repository, "direction", "out"}, strconv.FormatInt(st.Tokens.Output, 10)},
		)
	}
	return f
}

// costFamily sums each run's own estimate by repository, so the figure is
// what adding the runs up gives, not the estimate of the pooled usage.
func costFamily(runs []domain.Run, stats []domain.RepositoryStats, prices domain.PriceTable) family {
	costs := make(map[string]domain.Cost)
	for _, r := range runs {
		costs[r.Repository] += prices.Estimate(r.Compute, r.Tokens)
	}
	return perRepository("forja_estimated_cost_usd_micros",
		"Sum of the runs' estimated cost at the server's price table, in USD micros, by repository.", stats,
		func(st domain.RepositoryStats) string { return strconv.FormatInt(int64(costs[st.Repository]), 10) })
}

func perRepository(name, help string, stats []domain.RepositoryStats, value func(domain.RepositoryStats) string) family {
	f := family{name: name, help: help}
	for _, st := range stats {
		f.samples = append(f.samples, sample{[]string{"repository", st.Repository}, value(st)})
	}
	return f
}

func seconds(s float64) string {
	return strconv.FormatFloat(s, 'f', -1, 64)
}

func write(b *bytes.Buffer, f family) {
	b.WriteString("# HELP " + f.name + " " + f.help + "\n")
	b.WriteString("# TYPE " + f.name + " gauge\n")
	for _, s := range f.samples {
		b.WriteString(f.name)
		b.WriteByte('{')
		for i := 0; i+1 < len(s.labels); i += 2 {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(s.labels[i] + `="` + escape(s.labels[i+1]) + `"`)
		}
		b.WriteString("} " + s.value + "\n")
	}
}

// escape is a label value as the text format quotes it: a backslash, a
// double quote and a line feed each escaped with a backslash.
func escape(v string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(v)
}
