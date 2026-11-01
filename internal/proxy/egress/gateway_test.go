package egress_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
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
	"github.com/fuseone-io/fuseone-forja/internal/proxy/egress"
)

const runID = "0123456789abcdef0123456789abcdef"

var deniedAt = time.Date(2026, 11, 1, 9, 30, 0, 0, time.UTC)

func fixedClock() time.Time { return deniedAt }

// syncBuffer is the gateway's stdout as the daemon would keep it: every
// write lands, in order, whatever goroutine made it.
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

// plainDial reaches loopback, which the gateway's own dialer refuses: the
// tests' upstream is an httptest server on 127.0.0.1.
func plainDial(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

// neverDial fails the test when the gateway opens any upstream: a refused
// destination is refused before anything leaves.
func neverDial(t *testing.T) func(context.Context, string, string) (net.Conn, error) {
	return func(_ context.Context, _, addr string) (net.Conn, error) {
		t.Errorf("gateway dialled %s for a destination it must refuse", addr)
		return nil, errors.New("test: dial refused")
	}
}

type gatewayUnderTest struct {
	addr   string
	log    *syncBuffer
	cancel context.CancelFunc
	served chan error
}

// startGateway serves a gateway on a loopback port until the test ends and
// fails the test if Serve did not return cleanly by then.
func startGateway(t *testing.T, opts egress.Options) *gatewayUnderTest {
	t.Helper()
	log := &syncBuffer{}
	if opts.Log == nil {
		opts.Log = log
	}
	opts.RunID, opts.Now = runID, fixedClock
	g, err := egress.New(opts)
	if err != nil {
		t.Fatalf("egress.New: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	gw := &gatewayUnderTest{addr: ln.Addr().String(), log: log, cancel: cancel, served: make(chan error, 1)}
	go func() { gw.served <- g.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-gw.served:
			if err != nil {
				t.Errorf("Serve after cancel = %v, want nil", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Serve did not return after its context ended")
		}
	})
	return gw
}

// connect sends a raw CONNECT for target and returns the gateway's answer
// with the connection, which the test closes.
func connect(t *testing.T, gw *gatewayUnderTest, target string) (*http.Response, net.Conn) {
	t.Helper()
	conn, err := net.Dial("tcp", gw.addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err != nil {
		t.Fatal(err)
	}
	res, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("read CONNECT answer: %v", err)
	}
	return res, conn
}

func denials(t *testing.T, gw *gatewayUnderTest) domain.EgressReport {
	t.Helper()
	report, err := egress.ReadDenials(strings.NewReader(gw.log.String()), runID)
	if err != nil {
		t.Fatalf("ReadDenials: %v\nlog:\n%s", err, gw.log.String())
	}
	return report
}

func TestGateway_connectToUnlistedHost_403AndOneDenialLine(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, target, want string }{
		{name: "host not listed", target: "example.com:443", want: "example.com:443"},
		{name: "listed host on another port", target: "proxy.golang.org:80", want: "proxy.golang.org:80"},
		{name: "listed host as a suffix", target: "evilproxy.golang.org:443", want: "evilproxy.golang.org:443"},
		{name: "no port", target: "proxy.golang.org", want: "proxy.golang.org"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gw := startGateway(t, egress.Options{Allow: []string{"proxy.golang.org:443"}, Dial: neverDial(t)})

			res, _ := connect(t, gw, tt.target)
			if res.StatusCode != http.StatusForbidden {
				t.Fatalf("CONNECT %s = %d, want 403", tt.target, res.StatusCode)
			}
			report := denials(t, gw)
			want := domain.EgressDenial{At: deniedAt, Kind: domain.EgressConnect, Destination: tt.want}
			if report.Count != 1 || len(report.Denied) != 1 || report.Denied[0] != want {
				t.Fatalf("denials = %+v, want exactly [%+v]", report, want)
			}
			if lines := strings.Count(gw.log.String(), "\n"); lines != 2 {
				t.Errorf("log has %d lines, want the readiness line and one denial:\n%s", lines, gw.log.String())
			}
		})
	}
}

func TestGateway_plainHTTPRequest_deniedWithHostPortOnlyNoPath(t *testing.T) {
	t.Parallel()
	// Listing the host does not open plain HTTP: the allowlist admits
	// tunnels only.
	gw := startGateway(t, egress.Options{Allow: []string{"example.com:80"}, Dial: neverDial(t)})
	proxyURL := &url.URL{Scheme: "http", Host: gw.addr}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	req, err := http.NewRequest(http.MethodPost, "http://user:hunter2@Example.COM/patients/maria?token=s3cr3t", strings.NewReader("cpf=123.456.789-00"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Personal", "maria@example.org")

	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("request through the gateway: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("plain HTTP through the gateway = %d, want 403", res.StatusCode)
	}
	want := domain.EgressDenial{At: deniedAt, Kind: domain.EgressHTTP, Destination: "example.com:80"}
	if report := denials(t, gw); report.Count != 1 || report.Denied[0] != want {
		t.Fatalf("denials = %+v, want exactly [%+v]", report, want)
	}
	for _, leak := range []string{"patients", "maria", "token", "s3cr3t", "cpf", "123.456", "hunter2", "user"} {
		if strings.Contains(gw.log.String(), leak) {
			t.Errorf("log carries %q, want host:port only:\n%s", leak, gw.log.String())
		}
	}
}

// tlsUpstream is an HTTPS server on loopback that answers "tunnelled", and
// a client that trusts it and goes through gw.
func tlsUpstream(t *testing.T, gw func(allow string) *gatewayUnderTest) (*httptest.Server, *http.Client, *gatewayUnderTest) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "tunnelled")
	}))
	t.Cleanup(srv.Close)
	g := gw(srv.Listener.Addr().String())
	trusted := srv.Client().Transport.(*http.Transport).TLSClientConfig
	client := &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(&url.URL{Scheme: "http", Host: g.addr}),
		TLSClientConfig: trusted,
	}}
	return srv, client, g
}

func TestGateway_connectToListedHost_tunnelled(t *testing.T) {
	t.Parallel()
	srv, client, gw := tlsUpstream(t, func(allow string) *gatewayUnderTest {
		return startGateway(t, egress.Options{Allow: []string{allow}, Dial: plainDial})
	})

	res, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET %s through the gateway: %v", srv.URL, err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || string(body) != "tunnelled" {
		t.Fatalf("answer = %d %q, want 200 \"tunnelled\" from the upstream", res.StatusCode, body)
	}
	if report := denials(t, gw); report.Count != 0 {
		t.Fatalf("denials = %+v, want none for a listed destination", report)
	}
}

// A listed name that resolves to a private, loopback or link-local address
// is refused by the gateway's own dialer: it would reach another run's
// gateway on the shared bridge, or the node itself.
func TestGateway_listedHostResolvingToLoopback_deniedByDefaultDialer(t *testing.T) {
	t.Parallel()
	srv, client, gw := tlsUpstream(t, func(allow string) *gatewayUnderTest {
		return startGateway(t, egress.Options{Allow: []string{allow}})
	})

	_, err := client.Get(srv.URL)
	if err == nil {
		t.Fatal("GET through the gateway to a loopback address succeeded, want the tunnel refused")
	}
	want := domain.EgressDenial{At: deniedAt, Kind: domain.EgressConnect, Destination: srv.Listener.Addr().String()}
	if report := denials(t, gw); report.Count != 1 || report.Denied[0] != want {
		t.Fatalf("denials = %+v, want exactly [%+v]", report, want)
	}
}

func TestGateway_longDestination_truncatedToBound(t *testing.T) {
	t.Parallel()
	gw := startGateway(t, egress.Options{Dial: neverDial(t)})
	target := strings.Repeat("a", 3000) + ".example:443"

	res, _ := connect(t, gw, target)
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("CONNECT = %d, want 403", res.StatusCode)
	}
	report := denials(t, gw)
	if report.Count != 1 {
		t.Fatalf("denials = %+v, want one", report)
	}
	got := report.Denied[0].Destination
	if len(got) != egress.MaxDestinationBytes || !strings.HasPrefix(target, got) {
		t.Fatalf("destination len %d, want the first %d bytes of the target", len(got), egress.MaxDestinationBytes)
	}
}

// A request for the gateway itself is not an attempt to leave the
// workspace: it is answered and not counted.
func TestGateway_originFormRequest_400AndNotCounted(t *testing.T) {
	t.Parallel()
	gw := startGateway(t, egress.Options{})

	res, err := http.Get("http://" + gw.addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET / on the gateway = %d, want 400", res.StatusCode)
	}
	if report := denials(t, gw); report.Count != 0 {
		t.Fatalf("denials = %+v, want none", report)
	}
}

// Stopping the gateway ends the tunnels it holds: no goroutine copies on
// after Serve returned.
func TestGateway_serveCancelled_openTunnelClosed(t *testing.T) {
	t.Parallel()
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upstream.Close() })
	go func() {
		if c, err := upstream.Accept(); err == nil {
			defer func() { _ = c.Close() }()
			_, _ = io.Copy(io.Discard, c)
		}
	}()
	gw := startGateway(t, egress.Options{Allow: []string{upstream.Addr().String()}, Dial: plainDial})
	res, conn := connect(t, gw, upstream.Addr().String())
	if res.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT to a listed host = %d, want 200", res.StatusCode)
	}

	gw.cancel()
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = conn.Read(make([]byte, 1))
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatal("tunnel still open 10s after the gateway was stopped")
	}
}

// failingWriter accepts the readiness line and fails every write after it.
type failingWriter struct {
	mu     sync.Mutex
	writes int
}

func (w *failingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes++
	if w.writes > 1 {
		return 0, errors.New("stdout closed")
	}
	return len(p), nil
}

// A denial the log did not take is evidence lost: the gateway refuses the
// request and stops, so no later attempt goes unrecorded.
func TestGateway_logWriteFails_requestRefusedAndServeStops(t *testing.T) {
	t.Parallel()
	g, err := egress.New(egress.Options{RunID: runID, Now: fixedClock, Log: &failingWriter{}})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- g.Serve(context.Background(), ln) }()
	gw := &gatewayUnderTest{addr: ln.Addr().String()}

	res, _ := connect(t, gw, "example.com:443")
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("CONNECT = %d, want 403 even when the log fails", res.StatusCode)
	}
	select {
	case err := <-served:
		if err == nil || !strings.Contains(err.Error(), "stdout closed") {
			t.Fatalf("Serve = %v, want the log failure", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve still running after a denial could not be logged")
	}
}

func TestNew_invalidOptions_refused(t *testing.T) {
	t.Parallel()
	valid := egress.Options{RunID: runID, Now: fixedClock, Log: io.Discard}
	tests := []struct {
		name   string
		mutate func(*egress.Options)
	}{
		{name: "no run id", mutate: func(o *egress.Options) { o.RunID = "" }},
		{name: "no clock", mutate: func(o *egress.Options) { o.Now = nil }},
		{name: "no log", mutate: func(o *egress.Options) { o.Log = nil }},
		{name: "malformed allow entry", mutate: func(o *egress.Options) { o.Allow = []string{"https://proxy.golang.org"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opts := valid
			tt.mutate(&opts)
			if _, err := egress.New(opts); err == nil {
				t.Fatalf("New(%+v) = nil error", opts)
			}
		})
	}
	if _, err := egress.New(valid); err != nil {
		t.Fatalf("New(valid) = %v", err)
	}
}

func TestGateway_tlsHandshakeThroughTunnel_seesUpstreamCertificate(t *testing.T) {
	t.Parallel()
	srv, _, gw := tlsUpstream(t, func(allow string) *gatewayUnderTest {
		return startGateway(t, egress.Options{Allow: []string{allow}, Dial: plainDial})
	})
	res, conn := connect(t, gw, srv.Listener.Addr().String())
	if res.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT = %d, want 200", res.StatusCode)
	}
	cfg := srv.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	cfg.ServerName = "127.0.0.1"
	tlsConn := tls.Client(conn, cfg)
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("TLS handshake through the tunnel: %v", err)
	}
	if got := tlsConn.ConnectionState().PeerCertificates[0].Raw; !bytes.Equal(got, srv.Certificate().Raw) {
		t.Fatal("certificate through the tunnel is not the upstream's: the gateway terminated TLS")
	}
}
