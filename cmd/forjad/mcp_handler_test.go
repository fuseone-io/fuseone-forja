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

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fuseone-io/fuseone-forja/internal/caller"
)

type probeIn struct {
	Finding string `json:"finding"`
}

type probeOut struct {
	RunID string `json:"run_id"`
}

const probeFinding = "the user Maria Silva reported a leak"

// probeServer serves one tool that counts its calls and answers run_id r-42.
func probeServer(calls *atomic.Int32) *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "probe", Version: "test"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "forja.probe"}, func(context.Context, *sdk.CallToolRequest, probeIn) (*sdk.CallToolResult, probeOut, error) {
		calls.Add(1)
		return nil, probeOut{RunID: "r-42"}, nil
	})
	return server
}

func testRegistry(t *testing.T) caller.Registry {
	t.Helper()
	reg, err := caller.Parse([]byte(clientsYAML()))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return reg
}

func callBody(finding string) string {
	return `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"forja.probe","arguments":{"finding":"` + finding + `"}}}`
}

// post sends body to h as a Streamable HTTP client would, with authorization
// as the Authorization header when it is not empty.
func post(t *testing.T, h http.Handler, authorization, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "http://127.0.0.1"+mcpPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMCPHandler_knownBearer_toolRuns(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	h := mcpHandler(probeServer(&calls), testRegistry(t))
	if rec := post(t, h, "Bearer "+testBearer, callBody("x")); rec.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("status %d, calls %d: want 200 and the tool run once; body %q", rec.Code, calls.Load(), rec.Body.String())
	}
}

func TestMCPHandler_missingOrUnknownBearer_401NoToolRuns(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"missing":          "",
		"unknown":          "Bearer not-" + testBearer,
		"the hash":         "Bearer " + sha256Hex(testBearer),
		"another scheme":   "Basic " + testBearer,
		"bearer, no token": "Bearer",
		"bearer padded":    "Bearer " + testBearer + "x",
	}
	for name, authorization := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			h := mcpHandler(probeServer(&calls), testRegistry(t))
			rec := post(t, h, authorization, callBody("x"))
			if rec.Code != http.StatusUnauthorized || calls.Load() != 0 {
				t.Fatalf("status %d, calls %d: want 401 and no tool run", rec.Code, calls.Load())
			}
		})
	}
}

func TestMCPHandler_zeroRegistry_401(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	h := mcpHandler(probeServer(&calls), caller.Registry{})
	if rec := post(t, h, "Bearer "+testBearer, callBody("x")); rec.Code != http.StatusUnauthorized || calls.Load() != 0 {
		t.Fatalf("status %d, calls %d: want 401 with no clients", rec.Code, calls.Load())
	}
}

// paddedCall is a tools/call of exactly size bytes.
func paddedCall(t *testing.T, size int) string {
	t.Helper()
	body := callBody("")
	pad := size - len(body)
	if pad < 0 {
		t.Fatalf("size %d below the empty call", size)
	}
	return callBody(strings.Repeat("a", pad))
}

func TestMCPHandler_bodySize_413AboveOneMiB(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		size     int
		want     int
		wantRuns int32
	}{
		{name: "exactly 1 MiB", size: 1 << 20, want: http.StatusOK, wantRuns: 1},
		{name: "one byte over", size: 1<<20 + 1, want: http.StatusRequestEntityTooLarge},
		{name: "2 MiB, under the SDK default", size: 2 << 20, want: http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			h := mcpHandler(probeServer(&calls), testRegistry(t))
			rec := post(t, h, "Bearer "+testBearer, paddedCall(t, tt.size))
			if rec.Code != tt.want || calls.Load() != tt.wantRuns {
				t.Fatalf("status %d, calls %d; want %d and %d", rec.Code, calls.Load(), tt.want, tt.wantRuns)
			}
		})
	}
}

func TestMCPHandler_toolCall_loggedWithClientToolAndRunID(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var logs bytes.Buffer
	server := probeServer(&calls)
	server.AddReceivingMiddleware(logCalls(log.New(&logs, "", 0)))
	h := mcpHandler(server, testRegistry(t))
	for _, bearer := range []string{testBearer, stagedBearer} {
		if rec := post(t, h, "Bearer "+bearer, callBody(probeFinding)); rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
	}
	got := logs.String()
	for _, want := range []string{
		"client=fuseone-pilot tool=forja.probe run_id=r-42\n",
		"client=fuseone-staging tool=forja.probe run_id=r-42\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("log %q lacks %q", got, want)
		}
	}
	for _, secret := range []string{testBearer, stagedBearer, sha256Hex(testBearer), sha256Hex(stagedBearer), probeFinding, "platform-team"} {
		if strings.Contains(got, secret) {
			t.Fatalf("log %q carries %q", got, secret)
		}
	}
}

func TestMCPHandler_unknownTool_loggedWithoutItsName(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var logs bytes.Buffer
	server := probeServer(&calls)
	server.AddReceivingMiddleware(logCalls(log.New(&logs, "", 0)))
	h := mcpHandler(server, testRegistry(t))
	body := strings.Replace(callBody("x"), "forja.probe", "Maria Silva", 1)
	_ = post(t, h, "Bearer "+testBearer, body)
	if got := logs.String(); strings.Contains(got, "Maria") || !strings.Contains(got, "client=fuseone-pilot tool=- run_id=-") {
		t.Fatalf("log %q: an unknown tool's name is free text and must not be logged", got)
	}
}
