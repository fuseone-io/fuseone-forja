package registry_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/proxy/egress"
	"github.com/fuseone-io/fuseone-forja/internal/proxy/registry"
)

const (
	testRun   = "run-registry-1"
	testEntry = "example.com/area"
	username  = "forja-reader"
)

func testTime() time.Time { return time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC) }

// syncBuffer is a log the proxy's handlers write to concurrently.
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

// recorded is what an upstream saw of one request.
type recorded struct {
	host, path, rawQuery string
	header               http.Header
}

// upstream is a TLS registry that keeps every request it received.
type upstream struct {
	srv  *httptest.Server
	mu   sync.Mutex
	seen []recorded
}

func newUpstream(t *testing.T, h http.HandlerFunc) *upstream {
	t.Helper()
	u := &upstream{}
	u.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.seen = append(u.seen, recorded{host: r.Host, path: r.URL.Path, rawQuery: r.URL.RawQuery, header: r.Header.Clone()})
		u.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) requests() []recorded {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]recorded(nil), u.seen...)
}

func (u *upstream) addr() string { return u.srv.Listener.Addr().String() }

// requireCredential answers 401 unless the request carries exactly the
// configured Basic credential, the way a private registry does.
func requireCredential(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != username || p != canaryPassword {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// routes is a dial that sends each host:port to a listener of the test,
// and refuses every other address the way DialPublic refuses a private one.
func routes(to map[string]string) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		target, ok := to[addr]
		if !ok {
			return nil, fmt.Errorf("%s: %w", addr, egress.ErrNonPublicDestination)
		}
		var d net.Dialer
		return d.DialContext(ctx, network, target)
	}
}

func trust(u *upstream) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(u.srv.Certificate())
	return pool
}

// proxyFor is a proxy declaring testEntry that reaches u as example.com.
func proxyFor(u *upstream, extra map[string]string) registry.Options {
	to := map[string]string{"example.com:443": u.addr()}
	for k, v := range extra {
		to[k] = v
	}
	return registry.Options{
		RunID:       testRun,
		Registries:  []string{testEntry},
		Credentials: map[string]registry.Credential{testEntry: {Username: username, Password: canaryPassword}},
		Now:         testTime,
		Dial:        routes(to),
		RootCAs:     trust(u),
	}
}

// running is a proxy serving on a loopback port.
type running struct {
	url  string
	log  *syncBuffer
	stop func() error
}

func start(t *testing.T, opts registry.Options) *running {
	t.Helper()
	log := &syncBuffer{}
	if opts.Log == nil {
		opts.Log = log
	}
	p, err := registry.New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- p.Serve(ctx, ln) }()
	var once sync.Once
	var result error
	stop := func() error {
		once.Do(func() {
			cancel()
			result = <-served
		})
		return result
	}
	t.Cleanup(func() { _ = stop() })
	return &running{url: "http://" + ln.Addr().String(), log: log, stop: stop}
}

// rawClient leaves the body as it came: no added Accept-Encoding, no
// decompression, no redirect followed.
func rawClient() *http.Client {
	return &http.Client{
		Transport:     &http.Transport{DisableCompression: true, Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func get(t *testing.T, req *http.Request) (int, []byte, http.Header) {
	t.Helper()
	res, err := rawClient().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return res.StatusCode, body, res.Header
}

func newGet(t *testing.T, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// rawGet sends target exactly as written, with no client normalising it,
// and returns the status.
func rawGet(t *testing.T, base, target string) int {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: forja-registry\r\nConnection: close\r\n\r\n", target); err != nil {
		t.Fatal(err)
	}
	res, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("GET %s: %v", target, err)
	}
	_ = res.Body.Close()
	return res.StatusCode
}

// line is one line of the proxy's log, every field kept.
type line struct {
	Event    string    `json:"event"`
	Run      string    `json:"run"`
	At       time.Time `json:"at"`
	Registry string    `json:"registry"`
	Path     string    `json:"path"`
	Outcome  string    `json:"outcome"`
	Status   int       `json:"status"`
	Addr     string    `json:"addr"`
}

// accesses stops the proxy and returns its access lines, after checking
// the first line is the readiness of the right run.
func accesses(t *testing.T, r *running) []line {
	t.Helper()
	if err := r.stop(); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var lines []line
	sc := bufio.NewScanner(strings.NewReader(r.log.String()))
	for sc.Scan() {
		var l line
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Fatalf("log line %q: %v", sc.Text(), err)
		}
		lines = append(lines, l)
	}
	if len(lines) == 0 || lines[0].Event != "ready" || lines[0].Run != testRun || lines[0].Addr == "" {
		t.Fatalf("log = %q, want a readiness line of %s first", r.log.String(), testRun)
	}
	return lines[1:]
}

// failingLog accepts the readiness line and fails every write after it.
type failingLog struct {
	mu     sync.Mutex
	writes int
}

func (f *failingLog) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	if f.writes > 1 {
		return 0, errors.New("disk full")
	}
	return len(p), nil
}
