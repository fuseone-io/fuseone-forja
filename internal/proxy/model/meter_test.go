package model_test

import (
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// awaitUsageLines waits until the log holds n usage lines: a stream cut
// short is recorded when the proxy notices, not when the client does.
func awaitUsageLines(t *testing.T, p *proxyUnderTest, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(p.log.String(), `"event":"usage"`) < n {
		if time.Now().After(deadline) {
			t.Fatalf("log has fewer than %d usage lines:\n%s", n, p.log.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A stream's input is in message_start, with both cache fields, and its
// output in message_delta. message_start also says output_tokens 1; the
// delta's count replaces it, as the agent itself reports it.
func TestMeter_sse_inputFromStartOutputFromDelta(t *testing.T) {
	t.Parallel()
	sent := domain.TokenUsage{Input: 1, CacheCreation: 2, CacheRead: 4, Output: 8}
	p := startProxy(t, newUpstream(t, replySSE(sent)), 1_000_000)

	call(t, p, http.MethodPost, "/v1/messages", runToken)
	if got := readUsage(t, p).Tokens; got != sent {
		t.Fatalf("metered %+v, want %+v", got, sent)
	}
}

// A response with stream:false is one JSON message whose usage sits at the
// top level, in the same shape message_start carries it.
func TestMeter_json_topLevelUsage(t *testing.T) {
	t.Parallel()
	up := newUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_forja","type":"message","role":"assistant","model":"claude-opus-5-5",`+
			`"content":[{"type":"text","text":"forja-ok"}],"stop_reason":"end_turn","stop_sequence":null,`+
			`"usage":{"input_tokens":16,"cache_creation_input_tokens":32,"cache_read_input_tokens":64,"output_tokens":128}}`)
	})
	p := startProxy(t, up, 1_000_000)

	call(t, p, http.MethodPost, "/v1/messages", runToken)
	want := domain.TokenUsage{Input: 16, CacheCreation: 32, CacheRead: 64, Output: 128}
	if got := readUsage(t, p).Tokens; got != want {
		t.Fatalf("metered %+v, want %+v", got, want)
	}
}

// A stream that ends before message_delta still spent its input: the
// proxy counts what it saw.
func TestMeter_streamCutAfterStart_inputCounted(t *testing.T) {
	t.Parallel()
	sent := domain.TokenUsage{Input: 100, CacheCreation: 20, CacheRead: 3}
	up := newUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		events := sse(sent)
		_, _ = io.WriteString(w, events[:strings.Index(events, "\n\n")+2])
		w.(http.Flusher).Flush()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	})
	p := startProxy(t, up, 1_000_000)
	req, _ := http.NewRequest(http.MethodPost, "http://"+p.addr+"/v1/messages", strings.NewReader("{}"))
	req.Header.Set("x-api-key", runToken)
	if res, err := http.DefaultClient.Do(req); err == nil {
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
	}

	awaitUsageLines(t, p, 1)
	want := sent
	want.Output = 1
	if got := readUsage(t, p).Tokens; got != want {
		t.Fatalf("metered %+v, want the input of message_start %+v", got, want)
	}
}

// A client that goes away mid-stream stops the copy, not the count: what
// the proxy already relayed is recorded.
func TestMeter_clientGoneMidStream_inputCounted(t *testing.T) {
	t.Parallel()
	sent := domain.TokenUsage{Input: 9, CacheRead: 1}
	release := make(chan struct{})
	up := newUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		events := sse(sent)
		_, _ = io.WriteString(w, events[:strings.Index(events, "\n\n")+2])
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	defer close(release)
	p := startProxy(t, up, 1_000_000)
	req, _ := http.NewRequest(http.MethodPost, "http://"+p.addr+"/v1/messages", strings.NewReader("{}"))
	req.Header.Set("x-api-key", runToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	line := make([]byte, len("event: message_start\n"))
	if _, err := io.ReadFull(res.Body, line); err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()

	awaitUsageLines(t, p, 1)
	want := sent
	want.Output = 1
	if got := readUsage(t, p).Tokens; got != want {
		t.Fatalf("metered %+v, want what message_start said %+v", got, want)
	}
}

// sequenced answers the i-th request with the i-th usage.
type sequenced struct {
	mu    sync.Mutex
	next  int
	usage []domain.TokenUsage
}

func (s *sequenced) reply(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	u := s.usage[s.next]
	s.next++
	s.mu.Unlock()
	replySSE(u)(w, r)
}

// The run's total is the sum of every response's usage, whatever their
// number and their values.
func TestMeter_property_totalIsSumOfResponses(t *testing.T) {
	t.Parallel()
	// A fixed seed: a failure names the trial that reproduces it.
	rng := rand.New(rand.NewPCG(11, 2026))
	for trial := range 25 {
		n := 1 + rng.IntN(6)
		seq := &sequenced{}
		var want domain.TokenUsage
		for range n {
			u := domain.TokenUsage{Input: rng.Int64N(5000), CacheCreation: rng.Int64N(5000), CacheRead: rng.Int64N(5000), Output: rng.Int64N(5000)}
			seq.usage = append(seq.usage, u)
			want = want.Add(u)
		}
		t.Run(fmt.Sprintf("trial %d", trial), func(t *testing.T) {
			p := startProxy(t, newUpstream(t, seq.reply), 1<<40)
			for range n {
				call(t, p, http.MethodPost, "/v1/messages", runToken)
			}
			report := readUsage(t, p)
			if report.Tokens != want || report.Capped {
				t.Fatalf("over %d responses %+v: metered %+v capped %v, want %+v uncapped", n, seq.usage, report.Tokens, report.Capped, want)
			}
		})
	}
}
