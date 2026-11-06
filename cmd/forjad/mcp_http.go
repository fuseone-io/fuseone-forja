package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/caller"
	"github.com/fuseone-io/fuseone-forja/internal/mcp"
)

// mcpPath is where the MCP endpoint is served.
const mcpPath = "/mcp"

// maxRequestBody bounds a request body; a larger one is answered 413.
const maxRequestBody = 1 << 20

// The MCP server's bounds. Reading a request is bounded; writing a
// response is not, since a tool call lasts as long as its run and the
// run's own deadline bounds it. A stop cancels every call in flight, and
// the shutdown waits for a cancelled run's teardown, every step at its
// bound, plus a margin for what runs between the steps.
const (
	mcpReadHeaderTimeout = 10 * time.Second
	mcpReadTimeout       = 30 * time.Second
	mcpShutdownTimeout   = teardownSteps*mcp.FinishTimeout + teardownMargin
	teardownMargin       = 15 * time.Second
)

// teardownSteps is how many steps a cancelled run takes, one after the
// other, to end: reading its workspace's proxies, removing the workspace,
// recording the stage, recording the release's start, releasing the claim
// and recording the run's end. Each is bounded by mcp.FinishTimeout or by
// the workspace drivers' close bound of the same length.
const teardownSteps = 6

// mcpHandler serves server statelessly to the clients in reg: a request
// without a bearer whose hash is in reg is answered 401, and then one not
// on mcpProtocolVersion 400, before the server sees it. A call's context
// ends with its request.
func mcpHandler(server *sdk.Server, reg caller.Registry) http.Handler {
	streamable := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server },
		&sdk.StreamableHTTPOptions{Stateless: true, MaxRequestBodyBytes: maxRequestBody, PropagateRequestCancellation: true})
	// The bearers are static and rotate by restart, so a TokenInfo carries
	// no expiration, which the middleware rejects by default.
	bearer := auth.RequireBearerToken(verifier(reg), &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})
	mux := http.NewServeMux()
	mux.Handle(mcpPath, bearer(requireProtocol(withoutAuthorization(streamable))))
	return mux
}

// withoutAuthorization serves next a copy of the request without its
// Authorization header, so that no tool can read the bearer; the client it
// authenticated stays on the context.
func withoutAuthorization(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.Clone(r.Context())
		r.Header.Del("Authorization")
		next.ServeHTTP(w, r)
	})
}

// verifier authenticates a bearer against reg; the client's name is the
// TokenInfo's UserID.
func verifier(reg caller.Registry) auth.TokenVerifier {
	return func(_ context.Context, bearer string, _ *http.Request) (*auth.TokenInfo, error) {
		c, err := reg.Authenticate(bearer)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", auth.ErrInvalidToken, err)
		}
		return &auth.TokenInfo{UserID: c.Name}, nil
	}
}

// newMCPServer serves h, over TLS when tlsCfg is set.
func newMCPServer(h http.Handler, tlsCfg *tls.Config) *http.Server {
	return &http.Server{
		Handler:           h,
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: mcpReadHeaderTimeout,
		ReadTimeout:       mcpReadTimeout,
	}
}

// httpSinks is where serveMCPHTTP reports: every tool call and every
// refused Origin is logged on log, and ready is what /readyz answers.
type httpSinks struct {
	log   io.Writer
	ready *readiness
}

// serveMCPHTTP serves server over HTTP as cfg says until ctx ends. It is
// ready from the moment the listener opens until the drain starts. Every
// request's context ends with ctx, so a stop cancels the calls in flight
// and the drain waits for them to wind down. A listener that cannot be
// opened is an error naming the variable; a server stopped by ctx is not
// an error.
func (h host) serveMCPHTTP(ctx context.Context, server *sdk.Server, cfg httpConfig, out httpSinks) error {
	ln, err := h.listen(ctx, cfg.addr)
	if err != nil {
		return fmt.Errorf("%s: %w", EnvMCPAddr, err)
	}
	logger := log.New(out.log, "", 0)
	server.AddReceivingMiddleware(logCalls(logger))
	srv := newMCPServer(mcpStack(server, cfg, logger), cfg.tls)
	srv.BaseContext = func(net.Listener) context.Context { return ctx }
	// A server being stopped is no longer ready while it drains.
	srv.RegisterOnShutdown(func() { out.ready.serving.Store(false) })
	out.ready.serving.Store(true)
	defer out.ready.serving.Store(false)
	served := make(chan error, 1)
	go func() {
		if cfg.tls != nil {
			served <- srv.ServeTLS(ln, "", "")
			return
		}
		served <- srv.Serve(ln)
	}()
	return awaitMCP(ctx, srv, served, cfg.drain)
}

// awaitMCP shuts srv down when ctx ends and waits for Serve to return; it
// returns Serve's error unless that is the shutdown itself. A call still
// running after drain is cut off, and that is an error: the stop was not
// clean.
func awaitMCP(ctx context.Context, srv *http.Server, served <-chan error, drain time.Duration) error {
	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}
	drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drain)
	defer cancel()
	shutdown := srv.Shutdown(drainCtx)
	if shutdown != nil {
		_ = srv.Close()
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	if shutdown != nil {
		return fmt.Errorf("mcp: stopped with tool calls in flight: %w", shutdown)
	}
	return nil
}
