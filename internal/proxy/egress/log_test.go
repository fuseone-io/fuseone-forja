package egress_test

import (
	"strings"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/proxy/egress"
)

const readyLine = `{"event":"ready","run":"` + runID + `","at":"2026-11-01T09:30:00Z"}` + "\n"

func deniedLine(run, destination string) string {
	return `{"event":"denied","run":"` + run + `","at":"2026-11-01T09:31:00Z","kind":"connect","destination":"` + destination + `"}` + "\n"
}

func TestReadDenials_gatewayLog_readyIgnoredDenialsInOrder(t *testing.T) {
	t.Parallel()
	log := readyLine + deniedLine(runID, "example.com:443") + deniedLine(runID, "1.1.1.1:443")

	report, err := egress.ReadDenials(strings.NewReader(log), runID)
	if err != nil {
		t.Fatalf("ReadDenials: %v", err)
	}
	at := time.Date(2026, 11, 1, 9, 31, 0, 0, time.UTC)
	want := []domain.EgressDenial{
		{At: at, Kind: domain.EgressConnect, Destination: "example.com:443"},
		{At: at, Kind: domain.EgressConnect, Destination: "1.1.1.1:443"},
	}
	if report.Count != 2 || len(report.Denied) != 2 || report.Denied[0] != want[0] || report.Denied[1] != want[1] {
		t.Fatalf("report = %+v, want %+v", report, want)
	}
}

func TestReadDenials_emptyLog_emptyReport(t *testing.T) {
	t.Parallel()
	report, err := egress.ReadDenials(strings.NewReader(""), runID)
	if err != nil || report.Count != 0 || len(report.Denied) != 0 {
		t.Fatalf("ReadDenials(\"\") = %+v, %v; want an empty report", report, err)
	}
}

// The log is evidence: a line the reader does not understand, or one that
// belongs to another run, makes the report untrustworthy, not shorter.
func TestReadDenials_foreignOrMalformedLine_error(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"another run's line": readyLine + deniedLine("ffffffffffffffffffffffffffffffff", "example.com:443"),
		"not json":           readyLine + "panic: runtime error\n",
		"unknown event":      readyLine + `{"event":"allowed","run":"` + runID + `"}` + "\n",
		"unknown kind":       readyLine + strings.Replace(deniedLine(runID, "example.com:443"), `"connect"`, `"udp"`, 1),
	}
	for name, log := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if report, err := egress.ReadDenials(strings.NewReader(log), runID); err == nil {
				t.Fatalf("ReadDenials = %+v, nil error; want the log refused", report)
			}
		})
	}
}

func TestAwaitReady_readinessLineOfThisRun_returns(t *testing.T) {
	t.Parallel()
	if err := egress.AwaitReady(strings.NewReader(readyLine), runID); err != nil {
		t.Fatalf("AwaitReady: %v", err)
	}
}

func TestAwaitReady_streamEndsFirst_error(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"no output":        "",
		"another run":      strings.Replace(readyLine, runID, "ffffffffffffffffffffffffffffffff", 1),
		"usage, then exit": "usage: forjad serve\n",
	}
	for name, out := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := egress.AwaitReady(strings.NewReader(out), runID); err == nil {
				t.Fatalf("AwaitReady(%q) = nil, want an error: the gateway never said it was ready", out)
			}
		})
	}
}

func TestParseAllow_wellFormed_lowercasedInOrder(t *testing.T) {
	t.Parallel()
	tests := []struct {
		raw  string
		want []string
	}{
		{raw: "", want: nil},
		{raw: "Proxy.Golang.org:443", want: []string{"proxy.golang.org:443"}},
		{raw: "proxy.golang.org:443, sum.golang.org:443 ,storage.googleapis.com:443", want: []string{"proxy.golang.org:443", "sum.golang.org:443", "storage.googleapis.com:443"}},
		{raw: "[2001:db8::1]:443", want: []string{"[2001:db8::1]:443"}},
	}
	for _, tt := range tests {
		got, err := egress.ParseAllow(tt.raw)
		if err != nil {
			t.Errorf("ParseAllow(%q) = %v", tt.raw, err)
			continue
		}
		if strings.Join(got, ",") != strings.Join(tt.want, ",") || len(got) != len(tt.want) {
			t.Errorf("ParseAllow(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func TestParseAllow_malformedEntry_errorNamesIt(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"proxy.golang.org",
		"https://proxy.golang.org:443",
		"proxy.golang.org:443/path",
		"user@proxy.golang.org:443",
		"proxy.golang.org:0",
		"proxy.golang.org:65536",
		"proxy.golang.org:https",
		":443",
		"proxy.golang.org:443,,sum.golang.org:443",
		"*.golang.org:443",
	} {
		_, err := egress.ParseAllow(raw)
		if err == nil {
			t.Errorf("ParseAllow(%q) = nil error, want it refused", raw)
		}
	}
}
