package main

import (
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
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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
	for _, addr := range []string{"127.0.0.1:0", "127.0.0.9:8080", "[::1]:8080", "localhost:8080", "LocalHost:8080"} {
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

func TestNewMCPServer_readBoundedWriteUnbounded(t *testing.T) {
	t.Parallel()
	srv := newMCPServer(context.Background(), http.NotFoundHandler(), nil)
	if srv.ReadHeaderTimeout <= 0 || srv.ReadTimeout <= 0 {
		t.Fatalf("ReadHeaderTimeout %v, ReadTimeout %v: both must bound a request", srv.ReadHeaderTimeout, srv.ReadTimeout)
	}
	if srv.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout %v: a tool call lasts as long as its run", srv.WriteTimeout)
	}
}
