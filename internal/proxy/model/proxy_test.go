package model_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/proxy/model"
)

const (
	runID = "0123456789abcdef0123456789abcdef"
	// realKey and runToken are distinct and share no prefix, so a check
	// that compares only a prefix or confuses the two fails.
	realKey  = "sk-ant-real-key-0000-forja-test"
	runToken = "f0e1d2c3b4a5968778695a4b3c2d1e0ff0e1d2c3b4a5968778695a4b3c2d1e0f"
)

// capBody is the agent's contract for the cap refusal, byte for byte.
const capBody = `{"type":"error","error":{"type":"rate_limit_error","message":"forja: run token cap reached"}}`

var meteredAt = time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)

func fixedClock() time.Time { return meteredAt }

// syncBuffer is the proxy's stdout as the daemon would keep it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// upstream is a fake Messages API: it records every request it got and
// answers with reply.
type upstream struct {
	srv   *httptest.Server
	mu    sync.Mutex
	got   []*http.Request
	reply func(w http.ResponseWriter, r *http.Request)
}

func newUpstream(t *testing.T, reply func(w http.ResponseWriter, r *http.Request)) *upstream {
	t.Helper()
	u := &upstream{reply: reply}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		u.mu.Lock()
		u.got = append(u.got, r)
		u.mu.Unlock()
		u.reply(w, r)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) requests() []*http.Request {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]*http.Request(nil), u.got...)
}

func (u *upstream) url(t *testing.T) *url.URL {
	t.Helper()
	parsed, err := model.ParseUpstream(u.srv.URL)
	if err != nil {
		t.Fatalf("ParseUpstream(%s): %v", u.srv.URL, err)
	}
	return parsed
}

// sse is a streamed response as Claude Code accepted it, recorded in
// docs/contracts/claude-code-headless/streaming-and-usage.md, with the
// usage numbers made the test's own.
func sse(u domain.TokenUsage) string {
	return "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_forja","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":` +
		fmt.Sprintf(`{"input_tokens":%d,"output_tokens":1,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d}}}`, u.Input, u.CacheCreation, u.CacheRead) + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"forja-ok"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		fmt.Sprintf(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":%d}}`, u.Output) + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
}

func replySSE(u domain.TokenUsage) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = io.WriteString(w, sse(u))
	}
}

type proxyUnderTest struct {
	addr   string
	log    *syncBuffer
	served chan error
	cancel context.CancelFunc
}

// startProxy serves a proxy for runID on loopback until the test ends and
// fails the test if Serve did not return cleanly by then.
func startProxy(t *testing.T, up *upstream, capTokens int64) *proxyUnderTest {
	t.Helper()
	p := serveProxy(t, model.Options{Upstream: up.url(t), Cap: capTokens}, &syncBuffer{})
	t.Cleanup(func() {
		p.cancel()
		select {
		case err := <-p.served:
			if err != nil {
				t.Errorf("Serve after cancel = %v, want nil", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Serve did not return after its context ended")
		}
	})
	return p
}

// serveProxy starts the proxy with opts completed by the test's run id,
// key, token and clock, writing its log to log.
func serveProxy(t *testing.T, opts model.Options, log io.Writer) *proxyUnderTest {
	t.Helper()
	opts.RunID, opts.Key, opts.Token, opts.Now, opts.Log = runID, realKey, runToken, fixedClock, log
	p, err := model.New(opts)
	if err != nil {
		t.Fatalf("model.New: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	put := &proxyUnderTest{addr: ln.Addr().String(), served: make(chan error, 1), cancel: cancel}
	if buf, ok := log.(*syncBuffer); ok {
		put.log = buf
	}
	go func() { put.served <- p.Serve(ctx, ln) }()
	return put
}

// call sends one request to the proxy with the given x-api-key, or none
// when key is empty, and returns the answer with its body read.
func call(t *testing.T, p *proxyUnderTest, method, target, key string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, "http://"+p.addr+target, strings.NewReader(`{"max_tokens":1,"model":"claude-opus-5-5","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("x-api-key", key)
	}
	req.Header.Set("anthropic-version", "2023-06-01")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, target, err)
	}
	return res, string(body)
}

func readUsage(t *testing.T, p *proxyUnderTest) model.Report {
	t.Helper()
	report, err := model.ReadUsage(strings.NewReader(p.log.String()), runID)
	if err != nil {
		t.Fatalf("ReadUsage: %v\nlog:\n%s", err, p.log.String())
	}
	return report
}

func TestProxy_messages_reachUpstreamWithRealKeyQueryAndHeaders(t *testing.T) {
	t.Parallel()
	up := newUpstream(t, replySSE(domain.TokenUsage{Input: 3, Output: 5}))
	p := startProxy(t, up, 1_000_000)
	req, err := http.NewRequest(http.MethodPost, "http://"+p.addr+"/v1/messages?beta=true", strings.NewReader(`{"max_tokens":1,"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-api-key", runToken)
	req.Header.Set("authorization", "Bearer "+runToken)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "claude-code-20250219,effort-2025-11-24")
	req.Header.Set("accept-encoding", "gzip, deflate, br, zstd")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()

	got := up.requests()
	if res.StatusCode != http.StatusOK || len(got) != 1 {
		t.Fatalf("status %d, upstream got %d requests; want 200 and one", res.StatusCode, len(got))
	}
	r := got[0]
	checks := map[string][2]string{
		"path":              {r.URL.Path, "/v1/messages"},
		"query":             {r.URL.RawQuery, "beta=true"},
		"x-api-key":         {r.Header.Get("x-api-key"), realKey},
		"authorization":     {r.Header.Get("authorization"), ""},
		"anthropic-version": {r.Header.Get("anthropic-version"), "2023-06-01"},
		"anthropic-beta":    {r.Header.Get("anthropic-beta"), "claude-code-20250219,effort-2025-11-24"},
		"accept-encoding":   {r.Header.Get("accept-encoding"), "identity"},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("upstream %s = %q, want %q", name, c[0], c[1])
		}
	}
	if body, _ := io.ReadAll(r.Body); string(body) != `{"max_tokens":1,"stream":true}` {
		t.Errorf("upstream body = %q, want the request's own", body)
	}
}

// The agent reads each event as the model sends it: the proxy must not
// hold the stream until it ends.
func TestProxy_sse_deliveredEventByEvent(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	up := newUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		events := sse(domain.TokenUsage{Input: 2, Output: 3})
		first := strings.Index(events, "\n\n") + 2
		_, _ = io.WriteString(w, events[:first])
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
		_, _ = io.WriteString(w, events[first:])
	})
	p := startProxy(t, up, 1_000_000)
	req, _ := http.NewRequest(http.MethodPost, "http://"+p.addr+"/v1/messages", strings.NewReader(`{"max_tokens":1}`))
	req.Header.Set("x-api-key", runToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()

	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(res.Body).ReadString('\n')
		lines <- line
	}()
	select {
	case line := <-lines:
		if line != "event: message_start\n" {
			t.Fatalf("first line = %q, want the message_start event", line)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("message_start did not reach the client while the upstream held the rest")
	}
	close(release)
}

func TestProxy_hello_answeredLocally(t *testing.T) {
	t.Parallel()
	up := newUpstream(t, replySSE(domain.TokenUsage{}))
	p := startProxy(t, up, 1_000_000)

	res, _ := call(t, p, http.MethodHead, "/api/hello", "")
	if res.StatusCode != http.StatusOK || len(up.requests()) != 0 {
		t.Fatalf("HEAD /api/hello = %d with %d upstream calls, want 200 and none", res.StatusCode, len(up.requests()))
	}
}

// Every route but the metered one is refused before the upstream: one the
// proxy does not meter would spend tokens the cap never sees.
func TestProxy_unmeteredRoute_404NoUpstream(t *testing.T) {
	t.Parallel()
	tests := []struct{ method, target string }{
		{http.MethodGet, "/v1/messages"},
		{http.MethodPost, "/v1/messages/count_tokens"},
		{http.MethodPost, "/v1/messages/batches"},
		{http.MethodPost, "/v1/complete"},
		{http.MethodPost, "/v1/messages/"},
		{http.MethodPost, "/v1/messages%2Fcount_tokens"},
		{http.MethodGet, "/api/hello"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.target, func(t *testing.T) {
			t.Parallel()
			up := newUpstream(t, replySSE(domain.TokenUsage{Input: 1}))
			p := startProxy(t, up, 1_000_000)

			res, _ := call(t, p, tt.method, tt.target, runToken)
			if res.StatusCode != http.StatusNotFound || len(up.requests()) != 0 {
				t.Fatalf("%s %s = %d with %d upstream calls, want 404 and none", tt.method, tt.target, res.StatusCode, len(up.requests()))
			}
		})
	}
}

// Only this run's token opens the proxy; the real key itself does not, and
// what was presented is never written down.
func TestProxy_notTheRunToken_401NoUpstreamNotLogged(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"absent":             "",
		"another run's":      "a0a1a2a3a4a5a6a7a8a9aaabacadaeafa0a1a2a3a4a5a6a7a8a9aaabacadaeaf",
		"the real key":       realKey,
		"token with a tail":  runToken + "00",
		"token cut short":    runToken[:len(runToken)-1],
		"token in uppercase": strings.ToUpper(runToken),
	}
	for name, key := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			up := newUpstream(t, replySSE(domain.TokenUsage{Input: 1}))
			p := startProxy(t, up, 1_000_000)

			res, _ := call(t, p, http.MethodPost, "/v1/messages", key)
			if res.StatusCode != http.StatusUnauthorized || len(up.requests()) != 0 {
				t.Fatalf("x-api-key %s = %d with %d upstream calls, want 401 and none", name, res.StatusCode, len(up.requests()))
			}
			if key != "" && strings.Contains(p.log.String(), key) {
				t.Fatalf("the presented value is in the log:\n%s", p.log.String())
			}
		})
	}
}

// With the cap just above one response, the second request still goes
// (the total is below the cap when it arrives) and the third does not.
// The refusal stays.
func TestProxy_capReached_429WithoutUpstreamAndSticky(t *testing.T) {
	t.Parallel()
	one := domain.TokenUsage{Input: 40, CacheCreation: 20, CacheRead: 10, Output: 30}
	up := newUpstream(t, replySSE(one))
	p := startProxy(t, up, one.Total()+1)

	for i := 1; i <= 2; i++ {
		if res, _ := call(t, p, http.MethodPost, "/v1/messages", runToken); res.StatusCode != http.StatusOK {
			t.Fatalf("request %d = %d, want 200: the total was below the cap", i, res.StatusCode)
		}
	}
	assertCapRefusal(t, p, 3)
	assertCapRefusal(t, p, 4)
	if n := len(up.requests()); n != 2 {
		t.Fatalf("upstream got %d requests, want 2: a refusal never calls it", n)
	}
	if n := strings.Count(p.log.String(), `"event":"cap"`); n != 1 {
		t.Fatalf("%d cap lines, want exactly one:\n%s", n, p.log.String())
	}
	report := readUsage(t, p)
	if !report.Capped || report.Tokens != one.Add(one) {
		t.Fatalf("report = %+v, want capped with two responses' tokens %+v", report, one.Add(one))
	}
}

// assertCapRefusal sends the i-th request and checks it got the cap
// refusal.
func assertCapRefusal(t *testing.T, p *proxyUnderTest, i int) {
	t.Helper()
	res, body := call(t, p, http.MethodPost, "/v1/messages", runToken)
	if res.StatusCode != http.StatusTooManyRequests || body != capBody {
		t.Fatalf("request %d = %d %q, want 429 %s", i, res.StatusCode, body, capBody)
	}
	if res.Header.Get("Content-Type") != "application/json" {
		t.Errorf("request %d Content-Type = %q, want application/json", i, res.Header.Get("Content-Type"))
	}
}

// A total exactly at the cap is the cap reached: the next request is
// refused.
func TestProxy_totalEqualsCap_nextRefused(t *testing.T) {
	t.Parallel()
	one := domain.TokenUsage{Input: 7, Output: 3}
	up := newUpstream(t, replySSE(one))
	p := startProxy(t, up, one.Total())

	if res, _ := call(t, p, http.MethodPost, "/v1/messages", runToken); res.StatusCode != http.StatusOK {
		t.Fatalf("first request = %d, want 200", res.StatusCode)
	}
	if res, _ := call(t, p, http.MethodPost, "/v1/messages", runToken); res.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("request after the total reached the cap = %d, want 429", res.StatusCode)
	}
	if !readUsage(t, p).Capped {
		t.Fatalf("no cap line once the total equalled the cap:\n%s", p.log.String())
	}
}

// The upstream is never handed a response to compress: a body the proxy
// cannot read is a body it cannot meter, so one that comes compressed
// anyway is not forwarded.
func TestProxy_compressedResponse_502(t *testing.T) {
	t.Parallel()
	up := newUpstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = io.WriteString(w, "\x1f\x8b not really gzip")
	})
	p := startProxy(t, up, 1_000_000)

	res, body := call(t, p, http.MethodPost, "/v1/messages", runToken)
	if res.StatusCode != http.StatusBadGateway || strings.Contains(body, "not really gzip") {
		t.Fatalf("compressed upstream answer = %d %q, want 502 without its body", res.StatusCode, body)
	}
	// The upstream answered, so it spent: the request is charged its size
	// and its max_tokens of 1.
	size := int64(len(`{"max_tokens":1,"model":"claude-opus-5-5","stream":true}`))
	if got := readUsage(t, p).Tokens; got != (domain.TokenUsage{Input: size, Output: 1}) {
		t.Fatalf("metered %+v for an answer the proxy could not read, want size %d and max_tokens 1", got, size)
	}
}

// Whatever the route and the outcome, neither the key nor the token is
// ever written to the log.
func TestProxy_log_neverCarriesKeyOrToken(t *testing.T) {
	t.Parallel()
	one := domain.TokenUsage{Input: 5, Output: 5}
	up := newUpstream(t, replySSE(one))
	p := startProxy(t, up, one.Total())
	call(t, p, http.MethodHead, "/api/hello", "")
	call(t, p, http.MethodPost, "/v1/messages/count_tokens", runToken)
	call(t, p, http.MethodPost, "/v1/messages", realKey)
	call(t, p, http.MethodPost, "/v1/messages", runToken)
	call(t, p, http.MethodPost, "/v1/messages", runToken)
	gone := newUpstream(t, replySSE(one))
	gone.srv.Close()
	broken := startProxy(t, gone, 1_000_000)
	if res, _ := call(t, broken, http.MethodPost, "/v1/messages", runToken); res.StatusCode != http.StatusBadGateway {
		t.Fatalf("unreachable upstream = %d, want 502", res.StatusCode)
	}

	logs := p.log.String() + broken.log.String()
	for _, event := range []string{`"event":"ready"`, `"event":"usage"`, `"event":"cap"`} {
		if !strings.Contains(logs, event) {
			t.Errorf("log lacks %s, the test did not reach every outcome:\n%s", event, logs)
		}
	}
	for _, secret := range []string{realKey, runToken, runToken[:16], realKey[:16]} {
		if strings.Contains(logs, secret) {
			t.Fatalf("log carries %q:\n%s", secret, logs)
		}
	}
}

// failingLog accepts the readiness line, then fails every write.
type failingLog struct {
	mu     sync.Mutex
	writes int
}

func (f *failingLog) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	if f.writes == 1 {
		return len(p), nil
	}
	return 0, errors.New("disk full")
}

// A response whose usage cannot be recorded is tokens the cap never
// counts: the proxy stops rather than serve on unrecorded.
func TestProxy_usageLineUnwritable_serveStops(t *testing.T) {
	t.Parallel()
	up := newUpstream(t, replySSE(domain.TokenUsage{Input: 1, Output: 1}))
	p := serveProxy(t, model.Options{Upstream: up.url(t), Cap: 1_000_000}, &failingLog{})
	defer p.cancel()

	call(t, p, http.MethodPost, "/v1/messages", runToken)
	select {
	case err := <-p.served:
		if err == nil || !strings.Contains(err.Error(), "disk full") {
			t.Fatalf("Serve = %v, want the log failure", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve kept serving after a usage line could not be written")
	}
}

func TestNew_incompleteOptions_refused(t *testing.T) {
	t.Parallel()
	up, err := model.ParseUpstream("https://api.anthropic.com")
	if err != nil {
		t.Fatal(err)
	}
	full := model.Options{RunID: runID, Upstream: up, Key: realKey, Token: runToken, Cap: 10, Now: fixedClock, Log: io.Discard}
	tests := map[string]func(*model.Options){
		"no run id":   func(o *model.Options) { o.RunID = "" },
		"no upstream": func(o *model.Options) { o.Upstream = nil },
		"no key":      func(o *model.Options) { o.Key = "" },
		"no token":    func(o *model.Options) { o.Token = "" },
		"zero cap":    func(o *model.Options) { o.Cap = 0 },
		"no clock":    func(o *model.Options) { o.Now = nil },
		"no log":      func(o *model.Options) { o.Log = nil },
		"token = key": func(o *model.Options) { o.Token = o.Key },
	}
	if _, err := model.New(full); err != nil {
		t.Fatalf("New(complete options) = %v", err)
	}
	for name, mutate := range tests {
		opts := full
		mutate(&opts)
		if _, err := model.New(opts); err == nil {
			t.Errorf("New with %s = nil error, want it refused", name)
		}
	}
}

func TestParseUpstream(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]string{
		"":                           "https://api.anthropic.com",
		"https://api.anthropic.com":  "https://api.anthropic.com",
		"https://api.anthropic.com/": "https://api.anthropic.com/",
		"http://172.17.0.5:8000":     "http://172.17.0.5:8000",
	} {
		got, err := model.ParseUpstream(raw)
		if err != nil || got.String() != want {
			t.Errorf("ParseUpstream(%q) = %v, %v; want %s", raw, got, err, want)
		}
	}
	for _, raw := range []string{
		"api.anthropic.com",
		"ftp://api.anthropic.com",
		"https://",
		"https://api.anthropic.com/v1",
		"https://api.anthropic.com?x=1",
		"https://user:pass@api.anthropic.com",
		"https://api.anthropic.com#frag",
	} {
		if _, err := model.ParseUpstream(raw); err == nil {
			t.Errorf("ParseUpstream(%q) = nil error, want it refused", raw)
		}
	}
}
