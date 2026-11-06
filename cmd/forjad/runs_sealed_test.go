package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/mcp"
	"github.com/fuseone-io/fuseone-forja/internal/runstore"
)

// driverUser is the database user the driver's error names.
const driverUser = "forja-operator-db"

// driverFailure is what the real Postgres driver answers for a database it
// cannot reach: its text names the user and the address.
func driverFailure(t *testing.T) (error, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, cause := runstore.OpenPostgres(ctx, "postgres://"+driverUser+":pw@"+addr+"/forja?sslmode=disable")
	if cause == nil || !strings.Contains(cause.Error(), driverUser) {
		t.Fatalf("driver error = %v, want one naming %s: the test would check nothing", cause, driverUser)
	}
	return cause, addr
}

// downRuns fails every call with the driver's error, as a Postgres
// store whose database went away after the start does.
type downRuns struct{ cause error }

func (b downRuns) Get(context.Context, string) (domain.Run, error) { return domain.Run{}, b.cause }
func (b downRuns) GetOwned(context.Context, string, string) (domain.Run, error) {
	return domain.Run{}, b.cause
}
func (b downRuns) List(context.Context, domain.RunFilter) ([]domain.Run, error) { return nil, b.cause }
func (b downRuns) Put(context.Context, domain.Run) error                        { return b.cause }

// statusOverHTTP serves runs, sealed as serve seals its store, over HTTP,
// calls forja.status with args as the test client, and returns the raw
// answer and the operator's log.
func statusOverHTTP(t *testing.T, runs mcp.RunStore, args string) (string, string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	operator := &lockedBuffer{}
	cfg := httpConfig{addr: "127.0.0.1:0", clients: testRegistry(t), drain: time.Second}
	deps := mcp.Deps{Runs: sealRuns(runs, operator), Callers: registryCallers{reg: cfg.clients}, Now: time.Now, NewID: func() string { return "r" }}
	bound, returned := make(chan string, 1), make(chan error, 1)
	go func() {
		returned <- host{listen: capturingListen(bound)}.serveMCPHTTP(ctx, mcpHTTPServer(deps), cfg, httpSinks{log: &lockedBuffer{}, ready: new(readiness)})
	}()
	t.Cleanup(func() { cancel(); <-returned })
	body := toolCall("forja.status", args)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+<-bound+mcpPath, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	setCallHeaders(req, body)
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST forja.status: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw), operator.String()
}

// The run store failing reaches the HTTP client as a fixed message; the
// driver's text, which names the database's user and address, goes only
// to the operator's log.
func TestServeHTTP_runStoreFails_clientGetsFixedMessageDriverTextLogged(t *testing.T) {
	t.Parallel()
	cause, addr := driverFailure(t)
	for name, args := range map[string]string{"by id": `{"run_id":"run-1"}`, "list": `{}`} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			answer, logged := statusOverHTTP(t, downRuns{cause: cause}, args)
			if !strings.Contains(answer, errRunStoreUnavailable.Error()) || strings.Contains(answer, driverUser) || strings.Contains(answer, addr) {
				t.Fatalf("client got %s\nwant %q and neither %s nor %s", answer, errRunStoreUnavailable, driverUser, addr)
			}
			if !strings.Contains(logged, driverUser) {
				t.Fatalf("operator log %q: want the driver's cause", logged)
			}
		})
	}
}

// Sealing keeps another client's run and an id no run has the same answer,
// byte for byte: a run the store does not find is passed on, not sealed.
func TestServeHTTP_sealedStore_foreignAndUnknownIDAnsweredAlike(t *testing.T) {
	t.Parallel()
	theirs := runstore.NewMemory()
	run := domain.Run{ID: "run-of-another", Tool: domain.ToolDoctor, Repository: "acme/payments", Client: "fuseone-staging",
		State: domain.StateRunning, StartedAt: time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)}
	if err := theirs.Put(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	args := `{"run_id":"run-of-another"}`
	foreign, _ := statusOverHTTP(t, theirs, args)
	unknown, _ := statusOverHTTP(t, runstore.NewMemory(), args)
	if foreign != unknown || !strings.Contains(foreign, "isError") || strings.Contains(foreign, errRunStoreUnavailable.Error()) {
		t.Fatalf("another client's run = %s\nan unknown id      = %s\nwant the same not-found tool error", foreign, unknown)
	}
}

// serve's store is the sealed one, whatever it opened.
func TestHostStart_runStore_sealed(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	deps, closeRuns, err := host{getenv: envOf(map[string]string{EnvWorkspaceRuntime: "runc", EnvStateDir: t.TempDir()})}.start(context.Background(), &stderr)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer closeRuns()
	if _, ok := deps.Runs.(sealedRuns); !ok {
		t.Fatalf("serve's run store = %T, want it sealed", deps.Runs)
	}
}

// A run the store does not find, or one recorded under another client,
// is the store's own answer and is passed on; anything else is sealed.
func TestSealRuns_domainOutcomesPassedOnOthersSealed(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		cause  error
		sealed bool
	}{
		{domain.ErrRunNotFound, false},
		{domain.ErrRunClientChanged, false},
		{errors.New("failed to connect to user=" + driverUser), true},
	} {
		var operator bytes.Buffer
		err := sealRuns(downRuns{cause: tt.cause}, &operator).Put(context.Background(), domain.Run{})
		if got := errors.Is(err, errRunStoreUnavailable); got != tt.sealed || (!tt.sealed && !errors.Is(err, tt.cause)) {
			t.Fatalf("Put failing with %v = %v; want sealed %v", tt.cause, err, tt.sealed)
		}
		if tt.sealed && (strings.Contains(err.Error(), driverUser) || !strings.Contains(operator.String(), driverUser)) {
			t.Fatalf("sealed %v: client sees %q, operator log %q", tt.cause, err, operator.String())
		}
	}
}
