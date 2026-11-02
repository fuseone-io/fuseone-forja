// Package model is the proxy between a run's agent and the model API. It
// holds the real key, accepts only the run's own token, forwards the one
// metered route with the key swapped in, reads the usage of every response
// as it streams through and refuses the run once its tokens reach the cap.
// It writes one JSON line per metered response on its log; the control
// plane reads that log.
package model

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// Port is where the proxy listens inside the run's network.
const Port = "8080"

// readHeaderTimeout bounds how long a client may take to send a request.
const readHeaderTimeout = 10 * time.Second

// shutdownTimeout bounds the drain of in-flight requests on stop.
const shutdownTimeout = 5 * time.Second

// The refusals the proxy answers itself, in the Messages API error shape.
const (
	capBody          = `{"type":"error","error":{"type":"rate_limit_error","message":"forja: run token cap reached"}}`
	unauthorizedBody = `{"type":"error","error":{"type":"authentication_error","message":"forja: not this run's token"}}`
	notFoundBody     = `{"type":"error","error":{"type":"not_found_error","message":"forja: route not served"}}`
	stoppedBody      = `{"type":"error","error":{"type":"api_error","message":"forja: model proxy stopped"}}`
)

// Options configures a proxy.
type Options struct {
	// RunID stamps every log line, so a log can be checked to be this run's.
	RunID    string
	Upstream *url.URL
	// Key is the real API key; Token is the only x-api-key accepted.
	Key   string
	Token string
	// Cap is the run's total tokens, input and output. Once reached, every
	// request is refused.
	Cap int64
	Now func() time.Time
	// Log receives the readiness line, one line per metered response and
	// one when the cap is reached.
	Log io.Writer
	// Transport reaches the upstream; nil is a transport that ignores any
	// proxy variable of the environment.
	Transport http.RoundTripper
}

// Proxy is a configured model proxy, ready to Serve.
type Proxy struct {
	runID      string
	upstream   *url.URL
	key, token string
	cap        int64
	now        func() time.Time
	transport  http.RoundTripper

	mu      sync.Mutex
	log     io.Writer
	total   domain.TokenUsage
	capped  bool
	stopped bool
}

// New checks opts and builds the proxy.
func New(opts Options) (*Proxy, error) {
	switch {
	case opts.RunID == "" || opts.Now == nil || opts.Log == nil || opts.Upstream == nil:
		return nil, errors.New("model: a proxy needs a run id, an upstream, a clock and a log")
	case opts.Key == "" || opts.Token == "" || opts.Key == opts.Token:
		return nil, errors.New("model: a proxy needs a key and a distinct run token")
	case opts.Cap <= 0:
		return nil, errors.New("model: the token cap must be positive")
	}
	transport := opts.Transport
	if transport == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.Proxy = nil
		transport = t
	}
	return &Proxy{
		runID: opts.RunID, upstream: opts.Upstream, key: opts.Key, token: opts.Token,
		cap: opts.Cap, now: opts.Now, log: opts.Log, transport: transport,
	}, nil
}

// Serve writes the readiness line and serves on ln until ctx ends. It
// returns nil when ctx ended, and an error when a line could not be
// logged: a response whose usage is not recorded is tokens the cap never
// counts, so the proxy stops.
func (p *Proxy) Serve(ctx context.Context, ln net.Listener) error {
	if err := p.write(entry{Event: eventReady, Run: p.runID, At: p.now(), Addr: ln.Addr().String()}); err != nil {
		_ = ln.Close()
		return fmt.Errorf("model: write readiness: %w", err)
	}
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	srv := &http.Server{
		Handler:           &handler{p: p, forward: p.reverseProxy(stop)},
		ReadHeaderTimeout: readHeaderTimeout,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case err := <-served:
		return fmt.Errorf("model: serve: %w", err)
	case <-ctx.Done():
	}
	drain, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	_ = srv.Shutdown(drain)
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("model: serve: %w", err)
	}
	if cause := context.Cause(ctx); errors.Is(cause, errLogWrite) {
		return cause
	}
	return nil
}

// errLogWrite marks the cause of a stop that came from the log.
var errLogWrite = errors.New("model: write log")

// write appends one line; the caller may hold no lock of the proxy.
func (p *Proxy) write(e entry) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.writeLocked(e)
}

func (p *Proxy) writeLocked(e entry) error {
	_, err := p.log.Write(encode(e))
	return err
}

// admit reports whether a request may go upstream, and if not, the status
// and body of the refusal.
func (p *Proxy) admit() (status int, body string, ok bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.stopped:
		return http.StatusServiceUnavailable, stoppedBody, false
	case p.capped:
		return http.StatusTooManyRequests, capBody, false
	}
	return 0, "", true
}

// record adds one response's usage to the run and writes its line, and the
// cap line the first time the total reaches the cap. A failed write stops
// the proxy: nothing is admitted from then on.
func (p *Proxy) record(status int, u domain.TokenUsage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.total = p.total.Add(u)
	err := p.writeLocked(entry{Event: eventUsage, Run: p.runID, At: p.now(), Status: status, Usage: fieldOf(u)})
	if !p.capped && p.total.Total() >= p.cap {
		p.capped = true
		err = errors.Join(err, p.writeLocked(entry{Event: eventCap, Run: p.runID, At: p.now(), Total: p.total.Total(), Cap: p.cap}))
	}
	if err != nil {
		p.stopped = true
		return fmt.Errorf("%w: %w", errLogWrite, err)
	}
	return nil
}

// authorized compares the presented x-api-key with the run token in
// constant time.
func (p *Proxy) authorized(r *http.Request) bool {
	return subtle.ConstantTimeCompare([]byte(r.Header.Get("x-api-key")), []byte(p.token)) == 1
}
