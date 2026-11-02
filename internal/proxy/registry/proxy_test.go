package registry_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/proxy/registry"
)

// The workspace asks for /<entry>/<rest>; the upstream gets /<rest> of the
// entry's own path with the area's credential and the query untouched,
// and the body comes back byte for byte, encoding and all.
func TestProxy_declaredRegistry_upstreamReceivesCredential(t *testing.T) {
	t.Parallel()
	blob := make([]byte, 200<<10)
	_, _ = rand.Read(blob)
	u := newUpstream(t, requireCredential(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(blob)
	}))
	p := start(t, proxyFor(u, nil))

	status, body, header := get(t, newGet(t, p.url+"/example.com/area/pkg/@v/v1.0.0.zip?x=1&y=%2F"))
	if status != http.StatusOK || !bytes.Equal(body, blob) {
		t.Fatalf("GET = %d with %d bytes, want 200 with the upstream's %d bytes unmodified", status, len(body), len(blob))
	}
	if header.Get("Content-Encoding") != "gzip" || header.Get("Content-Type") != "application/zip" {
		t.Errorf("response headers = %v, want the upstream's encoding and type", header)
	}
	assertOneRequest(t, u, recorded{host: "example.com", path: "/area/pkg/@v/v1.0.0.zip", rawQuery: "x=1&y=%2F"})
	got := accesses(t, p)
	want := line{Event: "access", Run: testRun, At: testTime(), Registry: testEntry, Path: "/pkg/@v/v1.0.0.zip", Outcome: "allowed", Status: 200}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("access lines = %+v, want exactly %+v", got, want)
	}
}

// assertOneRequest checks u saw exactly one request, to want's host, path
// and query.
func assertOneRequest(t *testing.T, u *upstream, want recorded) {
	t.Helper()
	seen := u.requests()
	if len(seen) != 1 || seen[0].host != want.host || seen[0].path != want.path || seen[0].rawQuery != want.rawQuery {
		t.Fatalf("upstream saw %+v, want one request of %s%s?%s", seen, want.host, want.path, want.rawQuery)
	}
}

// Whatever credential or session the workspace sends is dropped; the
// upstream sees the area's credential only, and a session the upstream
// opens for it never reaches the workspace.
func TestProxy_clientAuthorization_replacedNotForwarded(t *testing.T) {
	t.Parallel()
	u := newUpstream(t, requireCredential(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Set-Cookie", "session=upstream-session-for-the-credential")
		_, _ = w.Write([]byte("ok"))
	}))
	p := start(t, proxyFor(u, nil))
	req := newGet(t, p.url+"/example.com/area/index")
	req.SetBasicAuth("intruder", "intruder-password")
	req.Header.Set("Proxy-Authorization", "Basic aW50cnVkZXI=")
	req.Header.Set("Cookie", "session=workspace-chosen")
	req.Header.Set("X-Tool", "kept")

	status, body, header := get(t, req)
	if status != http.StatusOK || string(body) != "ok" {
		t.Fatalf("GET = %d %q, want 200 ok: the client's Authorization must be replaced, not added to", status, body)
	}
	if header.Get("Set-Cookie") != "" {
		t.Errorf("Set-Cookie %q reached the workspace", header.Get("Set-Cookie"))
	}
	seen := u.requests()[0].header
	if seen.Get("Proxy-Authorization") != "" || seen.Get("Cookie") != "" || len(seen.Values("Authorization")) != 1 {
		t.Errorf("upstream headers = %v, want one Authorization and no Proxy-Authorization or Cookie", seen)
	}
	if seen.Get("X-Tool") != "kept" {
		t.Errorf("upstream headers = %v, want the tool's own headers forwarded", seen)
	}
}

// A redirect is followed by the proxy itself; the credential goes only to
// the host:port it belongs to.
func TestProxy_redirectToOtherHost_credentialNotSent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		location string
	}{
		{name: "other host", location: "https://127.0.0.1:%s/blob"},
		{name: "same host, other port", location: "https://example.com:%s/blob"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cdn := newUpstream(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("blob")) })
			port := cdn.addr()[strings.LastIndex(cdn.addr(), ":")+1:]
			origin := newUpstream(t, requireCredential(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, fmt.Sprintf(tt.location, port), http.StatusFound)
			}))
			opts := proxyFor(origin, map[string]string{"127.0.0.1:" + port: cdn.addr(), "example.com:" + port: cdn.addr()})
			p := start(t, opts)

			status, body, _ := get(t, newGet(t, p.url+"/example.com/area/zip"))
			if status != http.StatusOK || string(body) != "blob" {
				t.Fatalf("GET = %d %q, want the redirect followed to the blob", status, body)
			}
			if seen := cdn.requests(); len(seen) != 1 || seen[0].header.Get("Authorization") != "" {
				t.Fatalf("redirect target saw %+v, want one request without Authorization", seen)
			}
			if got := accesses(t, p); len(got) != 1 || got[0].Outcome != "allowed" || got[0].Status != 200 {
				t.Fatalf("access lines = %+v, want one allowed 200", got)
			}
		})
	}
}

// A redirect to an address the proxy may not dial, or away from TLS, is a
// denial: nothing is fetched and the attempt is logged.
func TestProxy_redirectToNonPublicOrPlainHTTP_denied(t *testing.T) {
	t.Parallel()
	for _, location := range []string{"https://10.0.0.7/blob", "http://example.com/blob"} {
		origin := newUpstream(t, requireCredential(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, location, http.StatusFound)
		}))
		p := start(t, proxyFor(origin, nil))

		if status, _, _ := get(t, newGet(t, p.url+"/example.com/area/zip")); status != http.StatusForbidden {
			t.Errorf("redirect to %s: GET = %d, want 403", location, status)
		}
		want := line{Event: "access", Run: testRun, At: testTime(), Registry: testEntry, Path: "/zip", Outcome: "denied", Status: 403}
		if got := accesses(t, p); len(got) != 1 || got[0] != want {
			t.Errorf("redirect to %s: access lines = %+v, want %+v", location, got, want)
		}
	}
}

// A path that could resolve outside the entry once the upstream cleans it
// is refused before matching, and never forwarded.
func TestProxy_dotDotOrEncodedSlash_denied(t *testing.T) {
	t.Parallel()
	u := newUpstream(t, requireCredential(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("leak")) }))
	p := start(t, proxyFor(u, nil))
	targets := []string{
		"/example.com/area/../other/pkg",
		"/example.com/area/%2e%2e/other/pkg",
		"/example.com/area/./pkg",
		"/example.com/area%2fpkg",
		"/example.com/area/a%2Fb",
		`/example.com/area/a\b`,
		"/example.com/area/a%5cb",
	}
	for _, target := range targets {
		if status := rawGet(t, p.url, target); status != http.StatusForbidden {
			t.Errorf("GET %s = %d, want 403", target, status)
		}
	}
	if seen := u.requests(); len(seen) != 0 {
		t.Fatalf("upstream saw %+v, want nothing forwarded", seen)
	}
	got := accesses(t, p)
	if len(got) != len(targets) {
		t.Fatalf("access lines = %+v, want one denial per request", got)
	}
	for _, l := range got {
		if l.Outcome != "denied" || l.Status != 403 {
			t.Errorf("access line %+v, want denied 403", l)
		}
	}
}

func TestProxy_prefixWithoutSegmentBoundary_denied(t *testing.T) {
	t.Parallel()
	u := newUpstream(t, requireCredential(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("leak")) }))
	p := start(t, proxyFor(u, nil))

	for _, target := range []string{"/example.com/area-evil/pkg", "/example.com/areax", "/example.com", "/"} {
		if status := rawGet(t, p.url, target); status != http.StatusForbidden {
			t.Errorf("GET %s = %d, want 403", target, status)
		}
	}
	if seen := u.requests(); len(seen) != 0 {
		t.Fatalf("upstream saw %+v, want nothing forwarded", seen)
	}
}

// An undeclared registry is a 403 logged as denied under the first
// segment asked for; the query, which may carry anything, is not logged.
func TestProxy_undeclaredRegistry_403AndLoggedDenied(t *testing.T) {
	t.Parallel()
	u := newUpstream(t, requireCredential(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("leak")) }))
	p := start(t, proxyFor(u, nil))

	if status := rawGet(t, p.url, "/other.example/pkg/x?token=workspace-query-secret"); status != http.StatusForbidden {
		t.Fatalf("GET undeclared = %d, want 403", status)
	}
	want := line{Event: "access", Run: testRun, At: testTime(), Registry: "other.example", Path: "/pkg/x", Outcome: "denied", Status: 403}
	if got := accesses(t, p); len(got) != 1 || got[0] != want {
		t.Fatalf("access lines = %+v, want %+v", got, want)
	}
	if strings.Contains(p.log.String(), "workspace-query-secret") {
		t.Fatalf("log carries the query: %s", p.log.String())
	}
}

// The workspace chooses how long a path is, not how much of the log one
// request takes.
func TestProxy_longPathAndRegistry_loggedBounded(t *testing.T) {
	t.Parallel()
	u := newUpstream(t, requireCredential(func(w http.ResponseWriter, _ *http.Request) {}))
	p := start(t, proxyFor(u, nil))
	long := strings.Repeat("x", 600)

	_ = rawGet(t, p.url, "/example.com/area/"+long)
	_ = rawGet(t, p.url, "/"+long+"/pkg")
	got := accesses(t, p)
	if len(got) != 2 || len(got[0].Path) != domain.MaxRegistryBytes || len(got[1].Registry) != domain.MaxRegistryBytes {
		t.Fatalf("access lines = %d, path %d bytes, registry %d bytes; want both cut to %d", len(got), len(got[0].Path), len(got[1].Registry), domain.MaxRegistryBytes)
	}
}

// An upstream that cannot be reached is the upstream's failure, not a
// policy decision: a 502, logged as allowed.
func TestProxy_upstreamUnreachable_502LoggedAllowed(t *testing.T) {
	t.Parallel()
	u := newUpstream(t, requireCredential(func(w http.ResponseWriter, _ *http.Request) {}))
	opts := proxyFor(u, nil)
	u.srv.Close()
	p := start(t, opts)

	if status := rawGet(t, p.url, "/example.com/area/pkg"); status != http.StatusBadGateway {
		t.Fatalf("GET = %d, want 502", status)
	}
	if got := accesses(t, p); len(got) != 1 || got[0].Outcome != "allowed" || got[0].Status != 502 {
		t.Fatalf("access lines = %+v, want one allowed 502", got)
	}
}

// A request the log cannot record gets no answer from upstream, and the
// proxy stops: nothing it serves afterwards would be on record.
func TestProxy_accessLineUnwritable_responseWithheldAndProxyStops(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"/example.com/area/pkg", "/undeclared.example/pkg"} {
		u := newUpstream(t, requireCredential(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("body")) }))
		opts := proxyFor(u, nil)
		opts.Log = &failingLog{}
		p := start(t, opts)

		if status := rawGet(t, p.url, target); status == http.StatusOK {
			t.Errorf("GET %s = 200 with an unwritable log, want the answer withheld", target)
		}
		if err := p.stop(); err == nil || !strings.Contains(err.Error(), "disk full") {
			t.Errorf("GET %s: Serve = %v, want it stopped by the log failure", target, err)
		}
	}
}

func TestNew_misconfigured_refused(t *testing.T) {
	t.Parallel()
	u := newUpstream(t, func(http.ResponseWriter, *http.Request) {})
	tests := map[string]func(*registry.Options){
		"no run id":         func(o *registry.Options) { o.RunID = "" },
		"no clock":          func(o *registry.Options) { o.Now = nil },
		"no log":            func(o *registry.Options) { o.Log = nil },
		"no registry":       func(o *registry.Options) { o.Registries = nil },
		"malformed entry":   func(o *registry.Options) { o.Registries = []string{"https://example.com"} },
		"credential absent": func(o *registry.Options) { o.Registries = append(o.Registries, "npm.pkg.github.com") },
	}
	for name, mutate := range tests {
		opts := proxyFor(u, nil)
		opts.Log = &syncBuffer{}
		mutate(&opts)
		if _, err := registry.New(opts); err == nil {
			t.Errorf("%s: New = nil error, want a refusal", name)
		}
	}
	opts := proxyFor(u, nil)
	opts.Log, opts.Registries = &syncBuffer{}, []string{testEntry, "npm.pkg.github.com"}
	if _, err := registry.New(opts); !errors.Is(err, registry.ErrCredentialMissing) || !strings.Contains(err.Error(), "npm.pkg.github.com") {
		t.Errorf("New without npm's credential = %v, want ErrCredentialMissing naming it", err)
	}
}
