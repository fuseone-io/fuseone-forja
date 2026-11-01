// Package egress is the gateway that stands between a run's workspace and
// the network: a forward proxy that tunnels CONNECT to an operator-listed
// host:port and refuses everything else, writing each refusal as one JSON
// line on its log. The gateway runs in a container of its own that the
// workspace cannot write to; the control plane reads the log.
package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// Port is where the gateway listens inside the run's network.
const Port = "3128"

// MaxDestinationBytes bounds the destination a denial records. The
// workspace chooses the name it asks for, so it does not choose how much
// of the log one attempt takes.
const MaxDestinationBytes = 255

// readHeaderTimeout bounds how long a client may take to send a request.
const readHeaderTimeout = 10 * time.Second

// shutdownTimeout bounds the drain of in-flight requests on stop.
const shutdownTimeout = 5 * time.Second

// Options configures a gateway.
type Options struct {
	// RunID stamps every log line, so a log can be checked to be this run's.
	RunID string
	// Allow lists the host:port a CONNECT may reach, matched exactly after
	// lowercasing. Empty, nothing leaves.
	Allow []string
	Now   func() time.Time
	// Log receives the readiness line and one line per denial.
	Log io.Writer
	// Dial opens the upstream of a tunnel; nil is DialPublic.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// Gateway is a configured forward proxy, ready to Serve.
type Gateway struct {
	runID string
	allow map[string]bool
	now   func() time.Time
	dial  func(ctx context.Context, network, addr string) (net.Conn, error)
	mu    sync.Mutex
	log   io.Writer
}

// New checks opts and builds the gateway.
func New(opts Options) (*Gateway, error) {
	if opts.RunID == "" || opts.Now == nil || opts.Log == nil {
		return nil, errors.New("egress: a gateway needs a run id, a clock and a log")
	}
	allow, err := ParseAllow(strings.Join(opts.Allow, ","))
	if err != nil {
		return nil, err
	}
	g := &Gateway{runID: opts.RunID, allow: map[string]bool{}, now: opts.Now, dial: opts.Dial, log: opts.Log}
	for _, a := range allow {
		g.allow[a] = true
	}
	if g.dial == nil {
		g.dial = DialPublic
	}
	return g, nil
}

// Serve writes the readiness line and serves on ln until ctx ends, which
// also closes every open tunnel. It returns nil when ctx ended, and an
// error when a denial could not be logged: from then on no attempt would
// be recorded, so the gateway stops and the workspace reaches nothing.
func (g *Gateway) Serve(ctx context.Context, ln net.Listener) error {
	if err := g.write(entry{Event: eventReady, Run: g.runID, At: g.now(), Addr: ln.Addr().String()}); err != nil {
		_ = ln.Close()
		return fmt.Errorf("egress: write readiness: %w", err)
	}
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	srv := &http.Server{
		Handler:           &handler{g: g, fail: stop},
		ReadHeaderTimeout: readHeaderTimeout,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case err := <-served:
		return fmt.Errorf("egress: serve: %w", err)
	case <-ctx.Done():
	}
	drain, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	// Hijacked tunnels are not the server's to drain; they end with ctx.
	_ = srv.Shutdown(drain)
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("egress: serve: %w", err)
	}
	if cause := context.Cause(ctx); errors.Is(cause, errLogWrite) {
		return cause
	}
	return nil
}

// errLogWrite marks the cause of a stop that came from the log.
var errLogWrite = errors.New("egress: write denial")

func (g *Gateway) write(e entry) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, err := g.log.Write(encode(e))
	return err
}

// deny records the refusal and answers 403. The answer goes out whether
// or not the line was written: a refusal is never turned into a pass.
func (g *Gateway) deny(w http.ResponseWriter, kind domain.EgressKind, destination string) error {
	err := g.write(entry{Event: eventDenied, Run: g.runID, At: g.now(), Kind: kind, Destination: bound(destination)})
	http.Error(w, "egress denied by forja", http.StatusForbidden)
	if err != nil {
		return fmt.Errorf("%w: %w", errLogWrite, err)
	}
	return nil
}

// bound cuts s to MaxDestinationBytes, dropping a rune split by the cut.
func bound(s string) string {
	if len(s) <= MaxDestinationBytes {
		return s
	}
	return strings.ToValidUTF8(s[:MaxDestinationBytes], "")
}

// handler serves one gateway's requests; fail stops the gateway.
type handler struct {
	g    *Gateway
	fail context.CancelCauseFunc
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var err error
	switch {
	case r.Method == http.MethodConnect:
		err = h.g.connect(w, r)
	case r.URL.IsAbs():
		err = h.g.deny(w, domain.EgressHTTP, httpDestination(r))
	default:
		http.Error(w, "forja egress gateway: proxy requests only", http.StatusBadRequest)
	}
	if err != nil {
		h.fail(err)
	}
}

// httpDestination is the host:port of an absolute-form request, the port
// filled in from the scheme when the URL leaves it out. Userinfo, path and
// query are never part of it.
func httpDestination(r *http.Request) string {
	port := r.URL.Port()
	if port == "" {
		port = map[string]string{"http": "80", "https": "443"}[strings.ToLower(r.URL.Scheme)]
	}
	return strings.ToLower(net.JoinHostPort(r.URL.Hostname(), port))
}
