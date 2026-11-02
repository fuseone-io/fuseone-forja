// Package registry is the proxy between a run's workspace and the private
// registries its contract declares. The workspace asks for
// http://<proxy>/<entry>/<rest>; the proxy forwards to https://<entry>/<rest>
// with the area's read credential, which it alone holds, and writes each
// request as one JSON line on its log. Forja guesses no ecosystem: the
// repository's setup points its own tool at the proxy. The proxy runs in
// a container of its own that the workspace cannot write to; the control
// plane reads the log.
package registry

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/proxy/egress"
)

// Port is where the proxy listens inside the run's network.
const Port = "8080"

// readHeaderTimeout bounds how long a client may take to send a request.
const readHeaderTimeout = 10 * time.Second

// shutdownTimeout bounds the drain of in-flight requests on stop.
const shutdownTimeout = 5 * time.Second

// ErrCredentialMissing is the refusal of a declared registry the operator
// gave no credential for.
var ErrCredentialMissing = errors.New("registry: no credential for a declared registry")

// Options configures a proxy.
type Options struct {
	// RunID stamps every log line, so a log can be checked to be this run's.
	RunID string
	// Registries are the contract's entries; a request outside all of them
	// is denied.
	Registries []string
	// Credentials holds one credential per entry; every declared entry
	// needs one. Entries not declared are ignored.
	Credentials map[string]Credential
	Now         func() time.Time
	// Log receives the readiness line and one line per request.
	Log io.Writer
	// Dial opens every upstream connection, redirects included; nil is
	// egress.DialPublic.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// RootCAs is the trust for upstream TLS; nil is the system's.
	RootCAs *x509.CertPool
}

// Proxy is a configured registry proxy, ready to Serve.
type Proxy struct {
	runID      string
	registries []string
	creds      map[string]Credential
	now        func() time.Time
	client     *http.Client
	mu         sync.Mutex
	log        io.Writer
}

// New checks opts and builds the proxy.
func New(opts Options) (*Proxy, error) {
	if opts.RunID == "" || opts.Now == nil || opts.Log == nil {
		return nil, errors.New("registry: a proxy needs a run id, a clock and a log")
	}
	if len(opts.Registries) == 0 {
		return nil, errors.New("registry: a proxy needs at least one declared registry")
	}
	p := &Proxy{runID: opts.RunID, creds: map[string]Credential{}, now: opts.Now, log: opts.Log}
	for i, entry := range opts.Registries {
		if err := domain.ValidRegistry(entry); err != nil {
			return nil, fmt.Errorf("registry: entry %d: %w", i, err)
		}
		c, ok := opts.Credentials[entry]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrCredentialMissing, entry)
		}
		p.registries, p.creds[entry] = append(p.registries, entry), c
	}
	dial := opts.Dial
	if dial == nil {
		dial = egress.DialPublic
	}
	p.client = newClient(dial, opts.RootCAs)
	return p, nil
}

// Serve writes the readiness line and serves on ln until ctx ends. It
// returns nil when ctx ended, and an error when a request could not be
// logged: from then on nothing would be on record, so the proxy stops.
func (p *Proxy) Serve(ctx context.Context, ln net.Listener) error {
	if err := p.write(entry{Event: eventReady, Run: p.runID, At: p.now(), Addr: ln.Addr().String()}); err != nil {
		_ = ln.Close()
		return fmt.Errorf("registry: write readiness: %w", err)
	}
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	srv := &http.Server{
		Handler:           &handler{p: p, fail: stop},
		ReadHeaderTimeout: readHeaderTimeout,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case err := <-served:
		return fmt.Errorf("registry: serve: %w", err)
	case <-ctx.Done():
	}
	drain, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	_ = srv.Shutdown(drain)
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("registry: serve: %w", err)
	}
	if cause := context.Cause(ctx); errors.Is(cause, errLogWrite) {
		return cause
	}
	return nil
}

// errLogWrite marks the cause of a stop that came from the log.
var errLogWrite = errors.New("registry: write access")

func (p *Proxy) write(e entry) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.log.Write(encode(e))
	return err
}

// record writes the access line of one request.
func (p *Proxy) record(registry, path string, outcome domain.RegistryOutcome, status int) error {
	err := p.write(entry{Event: eventAccess, Run: p.runID, At: p.now(), Registry: bound(registry), Path: bound(path), Outcome: outcome, Status: status})
	if err != nil {
		return fmt.Errorf("%w: %w", errLogWrite, err)
	}
	return nil
}

// deny records the refusal and answers 403. The answer goes out whether
// or not the line was written: a refusal is never turned into a pass.
func (p *Proxy) deny(w http.ResponseWriter, registry, path string) error {
	err := p.record(registry, path, domain.RegistryDenied, http.StatusForbidden)
	http.Error(w, "registry denied by forja", http.StatusForbidden)
	return err
}

// bound cuts s to MaxRegistryBytes, dropping a rune split by the cut.
func bound(s string) string {
	if len(s) <= domain.MaxRegistryBytes {
		return s
	}
	return strings.ToValidUTF8(s[:domain.MaxRegistryBytes], "")
}

// handler serves one proxy's requests; fail stops the proxy.
type handler struct {
	p    *Proxy
	fail context.CancelCauseFunc
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := h.p.serve(w, r); err != nil {
		h.fail(err)
	}
}

// serve refuses a path that could mean something else once an upstream
// cleans it, then matches the rest against the declared entries.
func (p *Proxy) serve(w http.ResponseWriter, r *http.Request) error {
	escaped := strings.TrimPrefix(r.URL.EscapedPath(), "/")
	if ambiguous(r.URL.Path, escaped) {
		first, rest := splitFirst(escaped)
		return p.deny(w, first, rest)
	}
	matched, ok := domain.MatchRegistry(p.registries, escaped)
	if !ok {
		first, rest := splitFirst(escaped)
		return p.deny(w, first, rest)
	}
	return p.forward(w, r, matched, escaped[len(matched):])
}

// ambiguous is a path with a dot segment once decoded, an encoded slash,
// or a backslash in either form.
func ambiguous(decoded, escaped string) bool {
	lower := strings.ToLower(escaped)
	if strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") || strings.Contains(decoded, `\`) {
		return true
	}
	for _, seg := range strings.Split(decoded, "/") {
		if seg == "." || seg == ".." {
			return true
		}
	}
	return false
}

// splitFirst is the first segment of path and what follows it, slash
// included.
func splitFirst(path string) (string, string) {
	if i := strings.IndexByte(path, '/'); i >= 0 {
		return path[:i], path[i:]
	}
	return path, ""
}
