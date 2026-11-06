package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/mcp"
)

// legacyCall is a tools/call as a client before 2026-07-28 sends it: no
// _meta, and the version, if any, only in the header.
const legacyCall = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"forja.probe","arguments":{"finding":"x"}}}`

// Over HTTP a POST reaches the server only with MCP-Protocol-Version
// 2026-07-28: missing, older, newer or malformed is answered 400 before
// any tool runs, whatever protocol the body speaks.
func TestMCPHandler_protocolVersionHeader_only2026ReachesTheTool(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		version  string
		body     string
		want     int
		wantRuns int32
	}{
		{name: "2026-07-28", version: protocol2026, body: callBody("x"), want: http.StatusOK, wantRuns: 1},
		{name: "missing, legacy call", version: "", body: legacyCall, want: http.StatusBadRequest},
		{name: "missing, 2026-07-28 call", version: "", body: callBody("x"), want: http.StatusBadRequest},
		{name: "2025-11-25", version: "2025-11-25", body: legacyCall, want: http.StatusBadRequest},
		{name: "2025-06-18", version: "2025-06-18", body: legacyCall, want: http.StatusBadRequest},
		{name: "2025-03-26", version: "2025-03-26", body: legacyCall, want: http.StatusBadRequest},
		{name: "a later version", version: "2027-01-01", body: callBody("x"), want: http.StatusBadRequest},
		{name: "not a version", version: "latest", body: legacyCall, want: http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			h := mcpHandler(probeServer(&calls), testRegistry(t))
			o := sendOptions{authorization: "Bearer " + testBearer, version: tt.version, method: http.MethodPost}
			rec := sendMCP(t, h, o, tt.body)
			if rec.Code != tt.want || calls.Load() != tt.wantRuns {
				t.Fatalf("status %d, calls %d; want %d and %d; body %q", rec.Code, calls.Load(), tt.want, tt.wantRuns, rec.Body.String())
			}
		})
	}
}

// The bearer is checked before the version: a request with neither learns
// only that it is not authenticated.
func TestMCPHandler_noBearerNoVersion_401NotVersionError(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	h := mcpHandler(probeServer(&calls), testRegistry(t))
	rec := sendMCP(t, h, sendOptions{method: http.MethodPost}, callBody("x"))
	if rec.Code != http.StatusUnauthorized || calls.Load() != 0 {
		t.Fatalf("status %d, calls %d: want 401 and no tool run", rec.Code, calls.Load())
	}
}

// The Origin is checked before the bearer and the version: a request from
// an Origin outside the list, with neither a bearer nor a version, or with
// a bearer and no version, is refused 403 and learns nothing else.
func TestMCPStack_badOriginNoVersion_403NotVersionError(t *testing.T) {
	t.Parallel()
	for name, authorization := range map[string]string{"no bearer": "", "valid bearer": "Bearer " + testBearer} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			h := originStack(t, allowedOrigin, &calls, &bytes.Buffer{})
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "http://127.0.0.1"+mcpPath, strings.NewReader(legacyCall))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			req.Header.Set("Origin", "https://evil.example")
			if authorization != "" {
				req.Header.Set("Authorization", authorization)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			refusedBare(t, rec)
			if calls.Load() != 0 {
				t.Fatalf("a tool ran %d times behind a refused Origin", calls.Load())
			}
		})
	}
}

// The version check is on POST, the only method a stateless server
// serves; another method keeps the server's own answer.
func TestMCPHandler_getWithoutVersion_methodNotAllowed(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	h := mcpHandler(probeServer(&calls), testRegistry(t))
	rec := sendMCP(t, h, sendOptions{authorization: "Bearer " + testBearer, method: http.MethodGet}, "")
	if rec.Code != http.StatusMethodNotAllowed || calls.Load() != 0 {
		t.Fatalf("status %d, calls %d: want 405 and no tool run", rec.Code, calls.Load())
	}
}

// The HTTP server advertises 2026-07-28 alone.
func TestMCPHTTPServer_discover_advertisesOnly2026(t *testing.T) {
	t.Parallel()
	h := mcpHandler(mcpHTTPServer(mcp.Deps{Version: "test"}), testRegistry(t))
	body := `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{` + requestMeta + `}}`
	rec := post(t, h, "Bearer "+testBearer, body)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"supportedVersions":["2026-07-28"]`) {
		t.Fatalf("status %d, body %q: want 200 advertising 2026-07-28 only", rec.Code, rec.Body.String())
	}
}

// A client that can only speak 2025-11-25 fails at connection time over
// HTTP, before it can call a tool.
func TestServe_httpClientPinnedTo20251125_refusedAtConnection(t *testing.T) {
	t.Parallel()
	endpoint, cancel, exited, _ := servedHTTP(t, map[string]string{}, "http")
	httpClient := &http.Client{Transport: bearerTransport{bearer: testBearer, base: http.DefaultTransport}}
	transport := &sdk.StreamableClientTransport{Endpoint: endpoint, HTTPClient: httpClient, MaxRetries: -1, DisableStandaloneSSE: true}
	client := sdk.NewClient(&sdk.Implementation{Name: "legacy", Version: "test"}, nil)
	cs, err := client.Connect(context.Background(), transport, &sdk.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err == nil {
		_ = cs.Close()
		t.Fatal("a client pinned to 2025-11-25 connected over HTTP")
	}
	cancel()
	<-exited
}

// stdio keeps every version the SDK speaks: a client pinned to an older
// one is answered with it.
func TestServe_stdioClientPinnedTo20250618_negotiatesIt(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	clientTransport, serverTransport := sdk.NewInMemoryTransports()
	exited := make(chan int, 1)
	go func() { exited <- serve(ctx, serverTransport, &lockedBuffer{}, quietEnv) }()
	client := sdk.NewClient(&sdk.Implementation{Name: "legacy", Version: "test"}, nil)
	cs, err := client.Connect(ctx, clientTransport, &sdk.ClientSessionOptions{ProtocolVersion: "2025-06-18"})
	if err != nil {
		t.Fatalf("connect over stdio: %v", err)
	}
	if got := cs.InitializeResult().ProtocolVersion; got != "2025-06-18" {
		t.Fatalf("stdio negotiated %q, want 2025-06-18", got)
	}
	_ = cs.Close()
	cancel()
	<-exited
}
