package model_test

import (
	"context"
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

// cutBody is the request of the tests whose response never says its
// output: the proxy charges its max_tokens, and its size when it never
// learnt the input either. It is already in the form the proxy forwards.
const (
	cutBody      = `{"max_tokens":4096,"messages":[]}`
	cutMaxTokens = 4096
)

// sendCut posts cutBody and reads whatever comes back.
func sendCut(t *testing.T, p *proxyUnderTest) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, "http://"+p.addr+"/v1/messages", strings.NewReader(cutBody))
	req.Header.Set("x-api-key", runToken)
	if res, err := http.DefaultClient.Do(req); err == nil {
		_, _ = io.Copy(io.Discard, res.Body)
		_ = res.Body.Close()
	}
}

// cutAfter answers a stream with body and then drops the connection.
func cutAfter(body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = io.WriteString(w, body)
		w.(http.Flusher).Flush()
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			_ = conn.Close()
		}
	}
}

// A stream that ends before message_delta still spent its input, and up to
// max_tokens of output the proxy never saw counted: it is charged both.
func TestMeter_streamCutAfterStart_inputAndMaxTokensCharged(t *testing.T) {
	t.Parallel()
	sent := domain.TokenUsage{Input: 100, CacheCreation: 20, CacheRead: 3}
	events := sse(sent)
	p := startProxy(t, newUpstream(t, cutAfter(events[:strings.Index(events, "\n\n")+2])), 1_000_000)
	sendCut(t, p)

	awaitUsageLines(t, p, 1)
	want := sent
	want.Output = cutMaxTokens
	if got := readUsage(t, p).Tokens; got != want {
		t.Fatalf("metered %+v, want the input of message_start and max_tokens %+v", got, want)
	}
}

// A stream cut before message_start told the proxy nothing: the request's
// size stands for its input and its max_tokens for its output.
func TestMeter_streamCutBeforeStart_sizeAndMaxTokensCharged(t *testing.T) {
	t.Parallel()
	p := startProxy(t, newUpstream(t, cutAfter("")), 1_000_000)
	sendCut(t, p)

	awaitUsageLines(t, p, 1)
	want := domain.TokenUsage{Input: int64(len(cutBody)), Output: cutMaxTokens}
	if got := readUsage(t, p).Tokens; got != want {
		t.Fatalf("metered %+v, want the body's size and max_tokens %+v", got, want)
	}
}

// A successful answer whose usage the proxy cannot read is charged as if
// it had used everything it was allowed; an error answer spent nothing.
func TestMeter_usageUnreadable_chargedOnlyOnSuccess(t *testing.T) {
	t.Parallel()
	whole := domain.TokenUsage{Input: int64(len(cutBody)), Output: cutMaxTokens}
	tests := map[string]struct {
		status      int
		contentType string
		body        string
		want        domain.TokenUsage
	}{
		"json that does not parse":  {http.StatusOK, "application/json", `{"usage":`, whole},
		"json without usage":        {http.StatusOK, "application/json", `{"type":"message"}`, whole},
		"json without output":       {http.StatusOK, "application/json", `{"usage":{"input_tokens":9}}`, domain.TokenUsage{Input: 9, Output: cutMaxTokens}},
		"neither stream nor json":   {http.StatusOK, "text/plain", `forja`, whole},
		"stream without any event":  {http.StatusOK, "text/event-stream", "event: ping\ndata: {\"type\":\"ping\"}\n\n", whole},
		"error answer":              {http.StatusBadRequest, "application/json", `{"type":"error","error":{"type":"invalid_request_error","message":"x"}}`, domain.TokenUsage{}},
		"overloaded answer":         {529, "application/json", `{"type":"error","error":{"type":"overloaded_error","message":"x"}}`, domain.TokenUsage{}},
		"error answer not readable": {http.StatusInternalServerError, "text/html", `<html>`, domain.TokenUsage{}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			up := newUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			})
			p := startProxy(t, up, 1_000_000)
			sendCut(t, p)

			awaitUsageLines(t, p, 1)
			if got := readUsage(t, p).Tokens; got != tt.want {
				t.Fatalf("metered %+v, want %+v", got, tt.want)
			}
		})
	}
}

// A client that leaves before the upstream answered, as it can with
// stream:false whose answer comes only once generated, left the upstream
// generating: it is charged the body's size and max_tokens.
func TestMeter_clientGoneBeforeAnswer_sizeAndMaxTokensCharged(t *testing.T) {
	t.Parallel()
	arrived := make(chan struct{})
	up := newUpstream(t, func(_ http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-r.Context().Done()
	})
	p := startProxy(t, up, 1_000_000)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-arrived
		cancel()
	}()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+p.addr+"/v1/messages", strings.NewReader(cutBody))
	req.Header.Set("x-api-key", runToken)
	if res, err := http.DefaultClient.Do(req); err == nil {
		_ = res.Body.Close()
		t.Fatalf("request answered %d, want it abandoned", res.StatusCode)
	}

	awaitUsageLines(t, p, 1)
	want := domain.TokenUsage{Input: int64(len(cutBody)), Output: cutMaxTokens}
	if got := readUsage(t, p).Tokens; got != want {
		t.Fatalf("metered %+v, want the body's size and max_tokens %+v", got, want)
	}
}

// An upstream the proxy cannot reach was sent nothing and spent nothing.
func TestMeter_upstreamUnreachable_nothingCharged(t *testing.T) {
	t.Parallel()
	gone := newUpstream(t, replySSE(domain.TokenUsage{Input: 1}))
	gone.srv.Close()
	p := startProxy(t, gone, 1_000_000)

	sendCut(t, p)
	if report := readUsage(t, p); report.Tokens != (domain.TokenUsage{}) {
		t.Fatalf("metered %+v for an unreachable upstream, want nothing", report.Tokens)
	}
}

// A stream cut inside message_start, after its data line is whole but
// before the blank line that ends the event, still spent that input.
func TestMeter_streamCutInsideStartEvent_inputCounted(t *testing.T) {
	t.Parallel()
	sent := domain.TokenUsage{Input: 70, CacheCreation: 6, CacheRead: 5}
	events := sse(sent)
	dataEnd := strings.Index(events, "\n\n")
	cuts := map[string]string{
		"after the data line":         events[:dataEnd+1],
		"data line without a newline": events[:dataEnd],
	}
	for name, body := range cuts {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := startProxy(t, newUpstream(t, cutAfter(body)), 1_000_000)
			sendCut(t, p)

			awaitUsageLines(t, p, 1)
			want := sent
			want.Output = cutMaxTokens
			if got := readUsage(t, p).Tokens; got != want {
				t.Fatalf("metered %+v, want the input of the cut message_start %+v", got, want)
			}
		})
	}
}

// A client that goes away mid-stream stops the copy, not the count: the
// input the proxy already relayed is recorded, and the output it will never
// see is charged at max_tokens, so leaving early saves the workspace
// nothing against the cap.
func TestMeter_clientGoneMidStream_inputAndMaxTokensCharged(t *testing.T) {
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
	req, _ := http.NewRequest(http.MethodPost, "http://"+p.addr+"/v1/messages", strings.NewReader(cutBody))
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
	want.Output = cutMaxTokens
	if got := readUsage(t, p).Tokens; got != want {
		t.Fatalf("metered %+v, want what message_start said and max_tokens %+v", got, want)
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
