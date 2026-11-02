package mcp_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	forja "github.com/fuseone-io/fuseone-forja/internal/mcp"
)

var apiContractNoLimits = []byte("version: 1\nimage: golang:1.25\ngates:\n  - name: build\n    run: make build\n")

// checkoutWithWallClock is the base checkout's contract with the given
// limits.wall_clock, or with no limits when wallClock is empty.
func checkoutWithWallClock(wallClock string) string {
	c := "version: 1\nimage: golang:1.26\ngates:\n  - name: build\n    run: make build\n"
	if wallClock != "" {
		c += "limits:\n  wall_clock: " + wallClock + "\n"
	}
	return c
}

// leaseCase is a run whose lease is sized from the API's contract and whose
// agent runs under the checkout's, elapsed after the claim was granted.
type leaseCase struct {
	name     string
	api      []byte
	checkout string
	lease    time.Duration
	elapsed  time.Duration
}

func newLeaseFixture(t *testing.T, tc leaseCase) *implementFixture {
	t.Helper()
	fx := newWiredFixture(t, tc.api, fixtureWiring{deps: func(d *forja.Deps) {
		d.Lease = tc.lease
		d.Now = func() time.Time { return startedAt().Add(tc.elapsed) }
	}})
	fx.cloner.co.files[".forja.yaml"] = tc.checkout
	return fx
}

func (f *fakeAgent) runs() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.tasks)
}

// The agent never runs past the claim's lease: a checkout whose contract
// asks more than the lease the run holds, or whose wall clock is already
// spent, fails the run before the agent starts and frees the issue.
func TestImplement_checkoutOutlivesHeldLease_agentNeverRuns(t *testing.T) {
	t.Parallel()
	tests := []leaseCase{
		{name: "checkout wall clock longer than the API's", api: contractWithWallClock, checkout: checkoutWithWallClock("6h")},
		{name: "checkout wall clock eats the lease's margin", api: contractWithWallClock, checkout: checkoutWithWallClock("31m")},
		{name: "checkout wall clock longer than a configured lease", api: contractWithWallClock, checkout: checkoutWithWallClock("6h"), lease: 50 * time.Minute},
		{name: "default wall clock longer than a configured lease", api: apiContractNoLimits, checkout: checkoutWithWallClock(""), lease: 10 * time.Minute},
		{name: "default wall clock as long as a configured lease", api: apiContractNoLimits, checkout: checkoutWithWallClock(""), lease: 30 * time.Minute},
		{name: "wall clock spent before the agent", api: contractWithWallClock, checkout: checkoutWithWallClock("20m"), elapsed: 20 * time.Minute},
		{name: "wall clock overspent before the agent", api: contractWithWallClock, checkout: checkoutWithWallClock("20m"), elapsed: 25 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fx := newLeaseFixture(t, tt)

			res := callImplement(t, fx.cs, implementIssue)
			if !res.IsError || !strings.Contains(textOf(res), "lease") {
				t.Fatalf("IsError = %v, text %q; want a tool error naming the lease", res.IsError, textOf(res))
			}
			if fx.agent.runs() != 0 {
				t.Fatalf("the agent ran %d times past the lease, want never", fx.agent.runs())
			}
			if got, want := fx.calls.list(), []string{"claim", "put running", "release", "put failed"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("calls = %q, want %q", got, want)
			}
		})
	}
}

// Within the lease, the agent's deadline is the checkout's wall clock
// counted from the claim, whatever lease the claim holds.
func TestImplement_checkoutWithinHeldLease_deadlineIsWallClockFromClaim(t *testing.T) {
	t.Parallel()
	tests := []struct {
		leaseCase
		want time.Duration
	}{
		{leaseCase: leaseCase{name: "checkout wall clock equal to the API's", api: contractWithWallClock, checkout: checkoutWithWallClock("30m")}, want: 30 * time.Minute},
		{leaseCase: leaseCase{name: "time before the agent comes off the wall clock", api: contractWithWallClock, checkout: checkoutWithWallClock("20m"), elapsed: 5 * time.Minute}, want: 15 * time.Minute},
		{leaseCase: leaseCase{name: "configured lease outlives the checkout's wall clock", api: contractWithWallClock, checkout: checkoutWithWallClock("40m"), lease: 50 * time.Minute}, want: 40 * time.Minute},
		{leaseCase: leaseCase{name: "default wall clock under the default lease", api: apiContractNoLimits, checkout: checkoutWithWallClock("")}, want: 30 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fx := newLeaseFixture(t, tt.leaseCase)

			decodeImplement(t, callImplement(t, fx.cs, implementIssue))
			if left := time.Until(fx.agent.deadline); left > tt.want || left < tt.want-time.Minute {
				t.Fatalf("agent deadline in %v, want %v", left, tt.want)
			}
		})
	}
}
