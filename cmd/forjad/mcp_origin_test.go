package main

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const allowedOrigin = "https://console.example.com"

// originStack is the production handler stack serving probeServer to the
// test clients, with allowed as FORJA_MCP_ALLOWED_ORIGINS.
func originStack(t *testing.T, allowed string, calls *atomic.Int32, logs *bytes.Buffer) http.Handler {
	t.Helper()
	origins, err := parseOrigins(allowed)
	if err != nil {
		t.Fatalf("parseOrigins(%q): %v", allowed, err)
	}
	cfg := httpConfig{clients: testRegistry(t), origins: origins}
	return mcpStack(probeServer(calls), cfg, log.New(logs, "", 0))
}

// send serves one request to h with the given method, Origin values (none
// when nil) and Authorization (none when empty).
func send(t *testing.T, h http.Handler, method string, origins []string, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), method, "http://127.0.0.1"+mcpPath, strings.NewReader(callBody("x")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if origins != nil {
		req.Header["Origin"] = origins
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// refusedBare fails unless rec is a 403 with no body, no WWW-Authenticate
// and no Access-Control-* header.
func refusedBare(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403; body %q", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body %q, want none", rec.Body.String())
	}
	for name := range rec.Header() {
		if name == "Www-Authenticate" || strings.HasPrefix(name, "Access-Control-") {
			t.Fatalf("header %s set on a refused Origin", name)
		}
	}
}

func TestMCPStack_originOutsideTheList_403BeforeTheBearer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		allowed       string
		method        string
		origins       []string
		authorization string
	}{
		{name: "O1 other origin, valid bearer", allowed: allowedOrigin, method: http.MethodPost, origins: []string{"https://evil.example"}, authorization: "Bearer " + testBearer},
		{name: "O1 empty list, valid bearer", allowed: "", method: http.MethodPost, origins: []string{allowedOrigin}, authorization: "Bearer " + testBearer},
		{name: "O2 null, valid bearer", allowed: allowedOrigin, method: http.MethodPost, origins: []string{"null"}, authorization: "Bearer " + testBearer},
		{name: "O5 other origin, no bearer", allowed: allowedOrigin, method: http.MethodPost, origins: []string{"https://evil.example"}},
		{name: "O5 other origin, unknown bearer", allowed: allowedOrigin, method: http.MethodPost, origins: []string{"https://evil.example"}, authorization: "Bearer not-" + testBearer},
		{name: "O6 preflight", allowed: allowedOrigin, method: http.MethodOptions, origins: []string{"https://evil.example"}},
		{name: "O6 preflight from an allowed origin", allowed: allowedOrigin, method: http.MethodOptions, origins: []string{allowedOrigin}},
		{name: "empty Origin value", allowed: allowedOrigin, method: http.MethodPost, origins: []string{""}, authorization: "Bearer " + testBearer},
		{name: "allowed host on another port", allowed: allowedOrigin, method: http.MethodPost, origins: []string{allowedOrigin + ":8443"}, authorization: "Bearer " + testBearer},
		{name: "allowed host over another scheme", allowed: allowedOrigin, method: http.MethodPost, origins: []string{"http://console.example.com"}, authorization: "Bearer " + testBearer},
		{name: "allowed origin with a path", allowed: allowedOrigin, method: http.MethodPost, origins: []string{allowedOrigin + "/"}, authorization: "Bearer " + testBearer},
		{name: "two Origin headers, one allowed", allowed: allowedOrigin, method: http.MethodPost, origins: []string{allowedOrigin, "https://evil.example"}, authorization: "Bearer " + testBearer},
		{name: "two Origin headers, both allowed", allowed: allowedOrigin, method: http.MethodPost, origins: []string{allowedOrigin, allowedOrigin}, authorization: "Bearer " + testBearer},
		{name: "GET stream", allowed: allowedOrigin, method: http.MethodGet, origins: []string{"https://evil.example"}, authorization: "Bearer " + testBearer},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			h := originStack(t, tt.allowed, &calls, &bytes.Buffer{})
			refusedBare(t, send(t, h, tt.method, tt.origins, tt.authorization))
			if calls.Load() != 0 {
				t.Fatalf("a tool ran %d times behind a refused Origin", calls.Load())
			}
		})
	}
}

func TestMCPStack_noOriginOrAllowedOrigin_reachesTheBearer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		allowed       string
		origins       []string
		authorization string
		want          int32
		wantRuns      int32
	}{
		{name: "O3 no Origin, valid bearer", allowed: "", authorization: "Bearer " + testBearer, want: http.StatusOK, wantRuns: 1},
		{name: "O3 no Origin, valid bearer, list set", allowed: allowedOrigin, authorization: "Bearer " + testBearer, want: http.StatusOK, wantRuns: 1},
		{name: "O4 allowed origin, valid bearer", allowed: allowedOrigin, origins: []string{allowedOrigin}, authorization: "Bearer " + testBearer, want: http.StatusOK, wantRuns: 1},
		{name: "O4 allowed origin, second entry", allowed: "http://127.0.0.1:3000, " + allowedOrigin, origins: []string{allowedOrigin}, authorization: "Bearer " + testBearer, want: http.StatusOK, wantRuns: 1},
		{name: "O4 allowed origin, case differs", allowed: "HTTPS://Console.Example.COM", origins: []string{"https://CONSOLE.example.com"}, authorization: "Bearer " + testBearer, want: http.StatusOK, wantRuns: 1},
		{name: "no Origin, no bearer", allowed: allowedOrigin, want: http.StatusUnauthorized},
		{name: "allowed origin, no bearer", allowed: allowedOrigin, origins: []string{allowedOrigin}, want: http.StatusUnauthorized},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			h := originStack(t, tt.allowed, &calls, &bytes.Buffer{})
			rec := send(t, h, http.MethodPost, tt.origins, tt.authorization)
			if int32(rec.Code) != tt.want || calls.Load() != tt.wantRuns {
				t.Fatalf("status %d, calls %d; want %d and %d; body %q", rec.Code, calls.Load(), tt.want, tt.wantRuns, rec.Body.String())
			}
		})
	}
}

func TestMCPStack_originRefused_loggedWithAddressAndOriginNeverTheBearer(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var logs bytes.Buffer
	h := originStack(t, allowedOrigin, &calls, &logs)
	_ = send(t, h, http.MethodPost, []string{"https://evil.example\nforjad: forged"}, "Bearer "+testBearer)
	got := logs.String()
	// httptest.NewRequest comes from 192.0.2.1:1234.
	for _, want := range []string{"192.0.2.1:1234", `"https://evil.example\nforjad: forged"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("log %q lacks %q", got, want)
		}
	}
	if strings.Count(got, "\n") != 1 {
		t.Fatalf("log %q: a refusal is one line, whatever the Origin carries", got)
	}
	for _, secret := range []string{testBearer, sha256Hex(testBearer)} {
		if strings.Contains(got, secret) {
			t.Fatalf("log %q carries the bearer or its hash", got)
		}
	}
}

func TestMCPStack_originAllowedOrAbsent_notLogged(t *testing.T) {
	t.Parallel()
	for _, origins := range [][]string{nil, {allowedOrigin}} {
		var calls atomic.Int32
		var logs bytes.Buffer
		_ = send(t, originStack(t, allowedOrigin, &calls, &logs), http.MethodPost, origins, "Bearer "+testBearer)
		if logs.Len() != 0 {
			t.Fatalf("Origin %v: log %q, want none", origins, logs.String())
		}
	}
}

func TestMCPHTTPConfig_allowedOriginsMalformed_errorNamingTheVariable(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"*",
		"https://*.example.com",
		"console.example.com",
		"https://console.example.com/",
		"https://console.example.com/mcp",
		"https://user@console.example.com",
		"https://console.example.com?x=1",
		"https://console.example.com#top",
		"https://console.example.com:",
		"https://console.example.com:https",
		"https://",
		"https://:443",
		"null",
		"https:console.example.com",
		allowedOrigin + ",",
		allowedOrigin + ",,http://127.0.0.1:3000",
		allowedOrigin + " http://127.0.0.1:3000",
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			env := map[string]string{EnvMCPAddr: "127.0.0.1:0", EnvClientsFile: writeFile(t, "c.yaml", clientsYAML()), EnvMCPAllowedOrigins: raw}
			if _, err := mcpHTTPConfig(envOf(env)); err == nil || !strings.Contains(err.Error(), EnvMCPAllowedOrigins) {
				t.Fatalf("mcpHTTPConfig = %v, want an error naming %s", err, EnvMCPAllowedOrigins)
			}
		})
	}
}

func TestServe_allowedOriginsMalformed_exitsOneNamingTheVariable(t *testing.T) {
	t.Parallel()
	env := map[string]string{EnvWorkspaceRuntime: "runc", EnvMCPAddr: "127.0.0.1:0", EnvClientsFile: writeFile(t, "c.yaml", clientsYAML()), EnvMCPAllowedOrigins: "https://*.example.com"}
	var stderr bytes.Buffer
	if code := (host{getenv: envOf(env), listen: refuseListen(t)}).serve(context.Background(), &sdk.StdioTransport{}, &stderr); code != 1 || !strings.Contains(stderr.String(), EnvMCPAllowedOrigins) {
		t.Fatalf("serve = %d, stderr %q; want 1 naming %s", code, stderr.String(), EnvMCPAllowedOrigins)
	}
}

func TestMCPHTTPConfig_allowedOrigins_readFromTheVariable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, raw string
		allowed   bool
	}{
		{name: "unset", raw: "", allowed: false},
		{name: "blank", raw: "  ", allowed: false},
		{name: "listed", raw: "http://127.0.0.1:3000 , " + allowedOrigin, allowed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := map[string]string{EnvMCPAddr: "127.0.0.1:0", EnvClientsFile: writeFile(t, "c.yaml", clientsYAML()), EnvMCPAllowedOrigins: tt.raw}
			cfg, err := mcpHTTPConfig(envOf(env))
			if err != nil {
				t.Fatalf("mcpHTTPConfig: %v", err)
			}
			if got := cfg.origins.allows(allowedOrigin); got != tt.allowed {
				t.Fatalf("allows(%s) = %v, want %v", allowedOrigin, got, tt.allowed)
			}
		})
	}
}

// rawPost posts a tools/list to endpoint with Origin and Authorization
// when they are not empty, and Host when it is not empty.
func rawPost(t *testing.T, endpoint, origin, authorization, hostHeader string) int {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	if hostHeader != "" {
		req.Host = hostHeader
	}
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = res.Body.Close()
	return res.StatusCode
}

// Over a real listener, serve puts the Origin check in front of the bearer
// and keeps the SDK's refusal of a non-loopback Host on a loopback listener.
func TestServe_httpOrigin_checkedBeforeTheBearer(t *testing.T) {
	t.Parallel()
	endpoint, cancel, exited, stderr := servedHTTP(t, map[string]string{EnvMCPAllowedOrigins: allowedOrigin}, "http")
	tests := []struct {
		name, origin, authorization, host string
		want                              int
	}{
		{name: "O1", origin: "https://evil.example", authorization: "Bearer " + testBearer, want: http.StatusForbidden},
		{name: "O5", origin: "https://evil.example", want: http.StatusForbidden},
		{name: "O3", authorization: "Bearer " + testBearer, want: http.StatusOK},
		{name: "O4", origin: allowedOrigin, authorization: "Bearer " + testBearer, want: http.StatusOK},
		{name: "no Origin, no bearer", want: http.StatusUnauthorized},
		{name: "non-loopback Host", authorization: "Bearer " + testBearer, host: "forja.evil.example", want: http.StatusForbidden},
	}
	for _, tt := range tests {
		if got := rawPost(t, endpoint, tt.origin, tt.authorization, tt.host); got != tt.want {
			t.Errorf("%s: status %d, want %d", tt.name, got, tt.want)
		}
	}
	cancel()
	if code := <-exited; code != 0 {
		t.Fatalf("serve after cancel = %d, want 0; stderr %q", code, stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, `"https://evil.example"`) || strings.Contains(got, testBearer) {
		t.Fatalf("stderr %q: want the refused Origin logged and never the bearer", got)
	}
}
