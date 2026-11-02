package model_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
	"github.com/fuseone-io/fuseone-forja/internal/proxy/model"
)

const readyLine = `{"event":"ready","run":"` + runID + `","at":"2026-11-02T10:00:00Z","addr":"[::]:8080"}` + "\n"

func usageLine(run string, input, output int) string {
	return `{"event":"usage","run":"` + run + `","at":"2026-11-02T10:00:01Z","status":200,"usage":{"input":` +
		strconv.Itoa(input) + `,"cache_creation":2,"cache_read":4,"output":` + strconv.Itoa(output) + `}}` + "\n"
}

const capLine = `{"event":"cap","run":"` + runID + `","at":"2026-11-02T10:00:02Z","total":40,"cap":30}` + "\n"

func TestReadUsage_log_sumsUsageAndSeesCap(t *testing.T) {
	t.Parallel()
	log := readyLine + usageLine(runID, 1, 8) + usageLine(runID, 16, 128) + capLine

	report, err := model.ReadUsage(strings.NewReader(log), runID)
	if err != nil {
		t.Fatalf("ReadUsage: %v", err)
	}
	want := domain.TokenUsage{Input: 17, CacheCreation: 4, CacheRead: 8, Output: 136}
	if report.Tokens != want || !report.Capped {
		t.Fatalf("report = %+v, want %+v capped", report, want)
	}
}

func TestReadUsage_noCapLine_notCapped(t *testing.T) {
	t.Parallel()
	report, err := model.ReadUsage(strings.NewReader(readyLine+usageLine(runID, 1, 1)), runID)
	if err != nil || report.Capped {
		t.Fatalf("ReadUsage = %+v, %v; want uncapped", report, err)
	}
}

func TestReadUsage_emptyLog_noTokens(t *testing.T) {
	t.Parallel()
	report, err := model.ReadUsage(strings.NewReader(""), runID)
	if err != nil || report != (model.Report{}) {
		t.Fatalf("ReadUsage(\"\") = %+v, %v; want an empty report", report, err)
	}
}

// The usage is evidence: a line the reader does not understand, or one
// stamped with another run, makes the whole report untrustworthy.
func TestReadUsage_foreignOrUnknownLine_refused(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"another run's usage": readyLine + usageLine("ffffffffffffffffffffffffffffffff", 1, 1),
		"another run's cap":   readyLine + strings.Replace(capLine, runID, "ffffffffffffffffffffffffffffffff", 1),
		"not json":            readyLine + "panic: runtime error\n",
		"unknown event":       readyLine + `{"event":"allowed","run":"` + runID + `"}` + "\n",
		"usage without usage": readyLine + `{"event":"usage","run":"` + runID + `","status":200}` + "\n",
		"negative tokens":     readyLine + strings.Replace(usageLine(runID, 1, 1), `"output":1`, `"output":-1`, 1),
	}
	for name, log := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if report, err := model.ReadUsage(strings.NewReader(log), runID); err == nil {
				t.Fatalf("ReadUsage = %+v, nil error; want the log refused", report)
			}
		})
	}
}

func TestAwaitReady_readinessLineOfThisRun_returns(t *testing.T) {
	t.Parallel()
	if err := model.AwaitReady(strings.NewReader(readyLine), runID); err != nil {
		t.Fatalf("AwaitReady: %v", err)
	}
}

func TestAwaitReady_streamEndsFirst_error(t *testing.T) {
	t.Parallel()
	for name, out := range map[string]string{
		"no output":   "",
		"another run": strings.Replace(readyLine, runID, "ffffffffffffffffffffffffffffffff", 1),
		"usage text":  "usage: forjad model\n",
	} {
		if err := model.AwaitReady(strings.NewReader(out), runID); err == nil {
			t.Errorf("AwaitReady(%s) = nil, want an error", name)
		}
	}
}

func TestAwaitCap_capLine_returnsNil(t *testing.T) {
	t.Parallel()
	log := readyLine + usageLine(runID, 1, 1) + capLine
	if err := model.AwaitCap(strings.NewReader(log), runID); err != nil {
		t.Fatalf("AwaitCap: %v", err)
	}
}

func TestAwaitCap_streamEndsWithoutCap_error(t *testing.T) {
	t.Parallel()
	for name, log := range map[string]string{
		"usage only":      readyLine + usageLine(runID, 1, 1),
		"another run cap": readyLine + strings.Replace(capLine, runID, "ffffffffffffffffffffffffffffffff", 1),
	} {
		if err := model.AwaitCap(strings.NewReader(log), runID); err == nil {
			t.Errorf("AwaitCap(%s) = nil, want an error: no cap of this run was logged", name)
		}
	}
}
