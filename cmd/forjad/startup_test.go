package main

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// silentDatabase accepts connections and never answers them: a database
// host that drops the conversation after the handshake.
func silentDatabase(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var held []net.Conn
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			held = append(held, c)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
		for _, c := range held {
			_ = c.Close()
		}
	})
	return ln.Addr().String()
}

// A database that never answers fails the start within the startup
// deadline, naming the variable, instead of holding the state directory
// with no server behind it. The URL sets no connect_timeout of its own.
func TestServe_databaseNeverAnswers_failsWithinStartupDeadline(t *testing.T) {
	t.Parallel()
	dsn := "postgres://forja:pw@" + silentDatabase(t) + "/forja?sslmode=disable"
	env := envOf(map[string]string{EnvDatabaseURL: dsn, EnvStateDir: t.TempDir(), EnvWorkspaceRuntime: "runc"})
	// Live, and longer than the deadline: only the startup deadline can end it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, serverTransport := sdk.NewInMemoryTransports()
	var stderr bytes.Buffer
	h := host{getenv: env, listen: listenTCP, startup: 300 * time.Millisecond}

	began := time.Now()
	code := h.serve(ctx, serverTransport, &stderr)

	if elapsed := time.Since(began); code != 1 || elapsed > 20*time.Second {
		t.Fatalf("serve = %d after %v, want 1 within the startup deadline; stderr %q", code, elapsed, stderr.String())
	}
	if !strings.Contains(stderr.String(), EnvDatabaseURL) || strings.Contains(stderr.String(), "pw@") {
		t.Fatalf("stderr %q: want it to name %s and not the URL", stderr.String(), EnvDatabaseURL)
	}
}

// Unset, the deadline is the default, never none.
func TestHost_startupDeadline_defaultWhenUnset(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		set  time.Duration
		want time.Duration
	}{
		"unset": {0, defaultStartupTimeout},
		"set":   {time.Second, time.Second},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := (host{startup: tt.set}).startupTimeout(); got != tt.want {
				t.Fatalf("startupTimeout = %v, want %v", got, tt.want)
			}
		})
	}
}
