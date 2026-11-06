package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	testBearer   = "test-bearer-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	stagedBearer = "staged-bearer-BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
)

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func clientsYAML() string {
	return "clients:\n" +
		"  - name: fuseone-pilot\n" +
		"    token_sha256: " + sha256Hex(testBearer) + "\n" +
		"    repositories: [acme/payments]\n" +
		"    rotation_owner: platform-team\n" +
		"  - name: fuseone-staging\n" +
		"    token_sha256: " + sha256Hex(stagedBearer) + "\n" +
		"    repositories: [acme/payments]\n" +
		"    rotation_owner: platform-team\n"
}

func writeFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// selfSigned writes a certificate for 127.0.0.1 and its key, and returns
// their paths and a pool that trusts the certificate.
func selfSigned(t *testing.T) (certFile, keyFile string, pool *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "forjad-test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool = x509.NewCertPool()
	pool.AddCert(cert)
	certFile = writeFile(t, "cert.pem", string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})))
	keyFile = writeFile(t, "key.pem", string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})))
	return certFile, keyFile, pool
}

func TestServe_httpMisconfigured_exitsOneNamingTheVariable(t *testing.T) {
	t.Parallel()
	clients := writeFile(t, "clients.yaml", clientsYAML())
	empty := writeFile(t, "empty.yaml", "")
	noEntries := writeFile(t, "none.yaml", "clients: []\n")
	malformed := writeFile(t, "bad.yaml", "clients:\n  - name: x\n    token_sha256: "+sha256Hex(testBearer)[:40]+"\n")
	cert, key, _ := selfSigned(t)
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "all interfaces, no certificate", env: map[string]string{EnvMCPAddr: ":8443", EnvClientsFile: clients}, want: EnvTLSCertFile},
		{name: "any address, no certificate", env: map[string]string{EnvMCPAddr: "0.0.0.0:8443", EnvClientsFile: clients}, want: EnvTLSCertFile},
		{name: "network address, no certificate", env: map[string]string{EnvMCPAddr: "10.0.0.7:8443", EnvClientsFile: clients}, want: EnvTLSCertFile},
		{name: "localhost, no certificate", env: map[string]string{EnvMCPAddr: "localhost:8443", EnvClientsFile: clients}, want: EnvTLSCertFile},
		{name: "host name, no certificate", env: map[string]string{EnvMCPAddr: "forja.internal:8443", EnvClientsFile: clients}, want: EnvTLSCertFile},
		{name: "not host:port", env: map[string]string{EnvMCPAddr: "forja please", EnvClientsFile: clients}, want: EnvMCPAddr},
		{name: "no clients file", env: map[string]string{EnvMCPAddr: "127.0.0.1:0"}, want: EnvClientsFile},
		{name: "clients file missing", env: map[string]string{EnvMCPAddr: "127.0.0.1:0", EnvClientsFile: filepath.Join(t.TempDir(), "absent.yaml")}, want: EnvClientsFile},
		{name: "clients file empty", env: map[string]string{EnvMCPAddr: "127.0.0.1:0", EnvClientsFile: empty}, want: EnvClientsFile},
		{name: "clients file without entries", env: map[string]string{EnvMCPAddr: "127.0.0.1:0", EnvClientsFile: noEntries}, want: EnvClientsFile},
		{name: "clients file malformed", env: map[string]string{EnvMCPAddr: "127.0.0.1:0", EnvClientsFile: malformed}, want: EnvClientsFile},
		{name: "certificate without key", env: map[string]string{EnvMCPAddr: "127.0.0.1:0", EnvClientsFile: clients, EnvTLSCertFile: cert}, want: EnvTLSKeyFile},
		{name: "key without certificate", env: map[string]string{EnvMCPAddr: "127.0.0.1:0", EnvClientsFile: clients, EnvTLSKeyFile: key}, want: EnvTLSCertFile},
		{name: "certificate unreadable", env: map[string]string{EnvMCPAddr: "127.0.0.1:0", EnvClientsFile: clients, EnvTLSCertFile: clients, EnvTLSKeyFile: key}, want: EnvTLSCertFile},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.env[EnvWorkspaceRuntime] = "runc"
			var stderr bytes.Buffer
			h := host{getenv: envOf(tt.env), listen: refuseListen(t)}
			if code := h.serve(context.Background(), &sdk.StdioTransport{}, &stderr); code != 1 {
				t.Fatalf("serve = %d, want 1; stderr %q", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.want) {
				t.Fatalf("stderr %q does not name %s", stderr.String(), tt.want)
			}
			if strings.Contains(stderr.String(), sha256Hex(testBearer)[:16]) {
				t.Fatalf("stderr %q carries a hash", stderr.String())
			}
		})
	}
}

func TestServe_httpPortTaken_exitsOneNamingTheVariable(t *testing.T) {
	t.Parallel()
	taken, err := listenTCP(context.Background(), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = taken.Close() })
	env := map[string]string{EnvWorkspaceRuntime: "runc", EnvMCPAddr: taken.Addr().String(), EnvClientsFile: writeFile(t, "c.yaml", clientsYAML())}
	var stderr bytes.Buffer
	if code := (host{getenv: envOf(env), listen: listenTCP}).serve(context.Background(), &sdk.StdioTransport{}, &stderr); code != 1 {
		t.Fatalf("serve = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), EnvMCPAddr) {
		t.Fatalf("stderr %q does not name %s", stderr.String(), EnvMCPAddr)
	}
}

func TestMCPHTTPConfig_networkAddressWithCertificate_accepted(t *testing.T) {
	t.Parallel()
	cert, key, _ := selfSigned(t)
	env := map[string]string{EnvMCPAddr: "0.0.0.0:8443", EnvClientsFile: writeFile(t, "c.yaml", clientsYAML()), EnvTLSCertFile: cert, EnvTLSKeyFile: key}
	cfg, err := mcpHTTPConfig(envOf(env))
	if err != nil {
		t.Fatalf("mcpHTTPConfig: %v", err)
	}
	if cfg.tls == nil || len(cfg.tls.Certificates) != 1 {
		t.Fatalf("a network address with a certificate must serve TLS, got %+v", cfg.tls)
	}
}

func TestMCPHTTPConfig_loopbackWithoutCertificate_plainHTTP(t *testing.T) {
	t.Parallel()
	for _, addr := range []string{"127.0.0.1:0", "127.0.0.9:8080", "[::1]:8080"} {
		env := map[string]string{EnvMCPAddr: addr, EnvClientsFile: writeFile(t, "c.yaml", clientsYAML())}
		cfg, err := mcpHTTPConfig(envOf(env))
		if err != nil {
			t.Fatalf("%s: %v", addr, err)
		}
		if cfg.addr != addr || cfg.tls != nil {
			t.Fatalf("%s: config %+v, want plain HTTP on the address", addr, cfg)
		}
	}
}

func TestMCPHTTPConfig_addrUnset_stdio(t *testing.T) {
	t.Parallel()
	cfg, err := mcpHTTPConfig(envOf(map[string]string{EnvClientsFile: "/nonexistent", EnvTLSCertFile: "/nonexistent"}))
	if err != nil || cfg.addr != "" {
		t.Fatalf("mcpHTTPConfig = (%+v, %v), want stdio and no error", cfg, err)
	}
}

// bearerTransport sets the Authorization header on every request.
type bearerTransport struct {
	bearer string
	base   http.RoundTripper
}

func (b bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+b.bearer)
	return b.base.RoundTrip(req)
}

// lockedBuffer is a stderr the server writes to while the test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// servedHTTP starts serve over HTTP on loopback with env added, and
// returns the endpoint, the cancel of serve's context, its exit code and
// its stderr.
func servedHTTP(t *testing.T, env map[string]string, scheme string) (string, context.CancelFunc, <-chan int, *lockedBuffer) {
	t.Helper()
	env[EnvWorkspaceRuntime] = "runc"
	env[EnvMCPAddr] = "127.0.0.1:0"
	env[EnvClientsFile] = writeFile(t, "clients.yaml", clientsYAML())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bound := make(chan string, 1)
	exited := make(chan int, 1)
	stderr := &lockedBuffer{}
	go func() {
		exited <- host{getenv: envOf(env), listen: capturingListen(bound)}.serve(ctx, &sdk.StdioTransport{}, stderr)
	}()
	select {
	case addr := <-bound:
		return scheme + "://" + addr + mcpPath, cancel, exited, stderr
	case code := <-exited:
		t.Fatalf("serve exited %d before listening: %s", code, stderr.String())
	}
	return "", cancel, exited, stderr
}

func connectHTTP(t *testing.T, endpoint, bearer string, base http.RoundTripper) (*sdk.ClientSession, error) {
	t.Helper()
	httpClient := &http.Client{Transport: bearerTransport{bearer: bearer, base: base}, Timeout: 10 * time.Second}
	transport := &sdk.StreamableClientTransport{Endpoint: endpoint, HTTPClient: httpClient, MaxRetries: -1, DisableStandaloneSSE: true}
	return sdk.NewClient(&sdk.Implementation{Name: "forjad-http-test", Version: "test"}, nil).Connect(context.Background(), transport, nil)
}

func TestServe_httpKnownBearerNoExpiration_listsTheFiveTools(t *testing.T) {
	t.Parallel()
	cert, key, pool := selfSigned(t)
	tests := []struct {
		name, scheme string
		env          map[string]string
		base         http.RoundTripper
	}{
		{name: "plain loopback", scheme: "http", env: map[string]string{}, base: http.DefaultTransport},
		{name: "TLS", scheme: "https", env: map[string]string{EnvTLSCertFile: cert, EnvTLSKeyFile: key},
			base: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			endpoint, cancel, exited, stderr := servedHTTP(t, tt.env, tt.scheme)
			for _, bearer := range []string{testBearer, stagedBearer} {
				cs, err := connectHTTP(t, endpoint, bearer, tt.base)
				if err != nil {
					t.Fatalf("connect: %v; stderr %q", err, stderr.String())
				}
				res, err := cs.ListTools(context.Background(), nil)
				if err != nil {
					t.Fatalf("tools/list: %v", err)
				}
				var names []string
				for _, tool := range res.Tools {
					names = append(names, tool.Name)
				}
				slices.Sort(names)
				if want := "forja.doctor,forja.implement,forja.plan,forja.status,forja.verify"; strings.Join(names, ",") != want {
					t.Fatalf("tools = %v, want %s", names, want)
				}
				_ = cs.Close()
			}
			cancel()
			if code := <-exited; code != 0 {
				t.Fatalf("serve after cancel = %d, want 0; stderr %q", code, stderr.String())
			}
			for _, secret := range []string{testBearer, stagedBearer, sha256Hex(testBearer), sha256Hex(stagedBearer)} {
				if strings.Contains(stderr.String(), secret) {
					t.Fatalf("stderr %q carries a bearer or its hash", stderr.String())
				}
			}
		})
	}
}

func TestServe_httpUnknownBearer_connectionRefused(t *testing.T) {
	t.Parallel()
	endpoint, cancel, exited, _ := servedHTTP(t, map[string]string{}, "http")
	if _, err := connectHTTP(t, endpoint, "not-"+testBearer, http.DefaultTransport); err == nil {
		t.Fatal("connect with an unknown bearer succeeded")
	}
	cancel()
	<-exited
}

func TestServe_httpPlainOnTLSListener_refused(t *testing.T) {
	t.Parallel()
	cert, key, _ := selfSigned(t)
	endpoint, cancel, exited, _ := servedHTTP(t, map[string]string{EnvTLSCertFile: cert, EnvTLSKeyFile: key}, "http")
	if _, err := connectHTTP(t, endpoint, testBearer, http.DefaultTransport); err == nil {
		t.Fatal("plain HTTP reached a server configured with a certificate")
	}
	cancel()
	<-exited
}

// blockingServer serves one tool that signals entered and returns once
// release is closed.
func blockingServer(entered, release chan struct{}) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "probe", Version: "test"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "forja.probe"}, func(context.Context, *sdk.CallToolRequest, probeIn) (*sdk.CallToolResult, probeOut, error) {
		close(entered)
		<-release
		return nil, probeOut{}, nil
	})
	return server
}

func TestServeMCPHTTP_stoppedWithCallInFlight_errorNotCleanExit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		finish  bool
		wantErr bool
	}{
		{name: "call outlives the drain", finish: false, wantErr: true},
		{name: "call finishes within the drain", finish: true, wantErr: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			entered, release := make(chan struct{}), make(chan struct{})
			t.Cleanup(func() {
				if !tt.finish {
					close(release)
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			bound, returned := make(chan string, 1), make(chan error, 1)
			cfg := httpConfig{addr: "127.0.0.1:0", clients: testRegistry(t), drain: 200 * time.Millisecond}
			go func() {
				returned <- host{listen: capturingListen(bound)}.serveMCPHTTP(ctx, blockingServer(entered, release), cfg, httpSinks{log: &lockedBuffer{}, ready: new(readiness)})
			}()
			go postCall(<-bound)
			<-entered
			if tt.finish {
				go func() { time.Sleep(20 * time.Millisecond); close(release) }()
			}
			cancel()
			if err := <-returned; (err != nil) != tt.wantErr {
				t.Fatalf("serveMCPHTTP = %v, want error %v", err, tt.wantErr)
			}
		})
	}
}

func postCall(addr string) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+addr+mcpPath, strings.NewReader(callBody("x")))
	if err != nil {
		return
	}
	setCallHeaders(req, callBody("x"))
	if res, err := http.DefaultClient.Do(req); err == nil {
		_ = res.Body.Close()
	}
}

// setCallHeaders makes req, carrying body, a 2026-07-28 Streamable HTTP
// POST of the test client.
func setCallHeaders(req *http.Request, body string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+testBearer)
	req.Header.Set("MCP-Protocol-Version", protocol2026)
	setStandardHeaders(req.Header, body)
}

func TestMCPHTTPConfig_drain_setForServe(t *testing.T) {
	t.Parallel()
	cfg, err := mcpHTTPConfig(envOf(map[string]string{EnvMCPAddr: "127.0.0.1:0", EnvClientsFile: writeFile(t, "c.yaml", clientsYAML())}))
	if err != nil || cfg.drain <= 0 {
		t.Fatalf("mcpHTTPConfig = (drain %v, %v), want a positive drain", cfg.drain, err)
	}
}

// servedHandler serves h on a loopback listener the way serve does, and
// returns its address.
func servedHandler(t *testing.T, h http.Handler, readTimeout time.Duration) string {
	t.Helper()
	ln, err := listenTCP(context.Background(), "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := newMCPServer(h, nil)
	srv.ReadTimeout = readTimeout
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

func TestMCPServer_callOutlastsTheReadTimeout_answered(t *testing.T) {
	t.Parallel()
	server := sdk.NewServer(&sdk.Implementation{Name: "probe", Version: "test"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "forja.probe"}, func(ctx context.Context, _ *sdk.CallToolRequest, _ probeIn) (*sdk.CallToolResult, probeOut, error) {
		select {
		case <-time.After(600 * time.Millisecond):
			return nil, probeOut{RunID: "r-slow"}, nil
		case <-ctx.Done():
			return nil, probeOut{}, ctx.Err()
		}
	})
	addr := servedHandler(t, mcpHandler(server, testRegistry(t)), 100*time.Millisecond)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+addr+mcpPath, strings.NewReader(callBody("x")))
	setCallHeaders(req, callBody("x"))
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil || res.StatusCode != http.StatusOK || !strings.Contains(string(body), "r-slow") {
		t.Fatalf("status %d, body %q, err %v: a call longer than ReadTimeout must still be answered", res.StatusCode, body, err)
	}
}

func TestMCPServer_bodyStallsPastTheReadTimeout_refusedNoToolRuns(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	addr := servedHandler(t, mcpHandler(probeServer(&calls), testRegistry(t)), 100*time.Millisecond)
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = io.WriteString(conn, "POST "+mcpPath+" HTTP/1.1\r\nHost: "+addr+"\r\nAuthorization: Bearer "+testBearer+
		"\r\nMCP-Protocol-Version: "+protocol2026+
		"\r\nContent-Type: application/json\r\nAccept: application/json, text/event-stream\r\nContent-Length: 500\r\n\r\n{\"jsonrpc\"")
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	status, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || !strings.Contains(status, " 400 ") || calls.Load() != 0 {
		t.Fatalf("status line %q, err %v, calls %d: a stalled body must be refused", status, err, calls.Load())
	}
}

func TestNewMCPServer_readBoundedWriteUnbounded(t *testing.T) {
	t.Parallel()
	srv := newMCPServer(http.NotFoundHandler(), nil)
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 {
		t.Fatalf("ReadHeaderTimeout %v, ReadTimeout %v: both must bound a request", srv.ReadHeaderTimeout, srv.ReadTimeout)
	}
	if srv.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout %v: a tool call lasts as long as its run", srv.WriteTimeout)
	}
}

// Over HTTP, /readyz holds once the MCP listener is open and drops as soon
// as the drain starts, while a call is still in flight.
func TestServeMCPHTTP_listenerOpen_readyUntilDrainStarts(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bound, returned := make(chan string, 1), make(chan error, 1)
	ready := new(readiness)
	cfg := httpConfig{addr: "127.0.0.1:0", clients: testRegistry(t), drain: 5 * time.Second}
	go func() {
		returned <- host{listen: capturingListen(bound)}.serveMCPHTTP(ctx, blockingServer(entered, release), cfg, httpSinks{log: &lockedBuffer{}, ready: ready})
	}()
	go postCall(<-bound)
	<-entered
	if !ready.serving.Load() {
		t.Fatal("not ready while the MCP listener serves a call")
	}
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for ready.serving.Load() {
		if time.Now().After(deadline) {
			t.Fatal("still ready after the drain started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(release)
	if err := <-returned; err != nil {
		t.Fatalf("serveMCPHTTP = %v, want a clean stop", err)
	}
}
