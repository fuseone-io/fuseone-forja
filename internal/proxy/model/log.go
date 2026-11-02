package model

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// The events of the proxy's log, one JSON object per line on stdout.
const (
	eventReady = "ready"
	eventUsage = "usage"
	eventCap   = "cap"
)

// maxLineBytes bounds one log line on read; every line the proxy writes is
// a handful of numbers.
const maxLineBytes = 4 << 10

// entry is one line of the log. It names no body, prompt, header, key or
// token: only what the control plane counts. The writer and the reader of
// the format are this package, so the two cannot drift apart.
type entry struct {
	Event string    `json:"event"`
	Run   string    `json:"run"`
	At    time.Time `json:"at"`
	// Addr is where a ready proxy listens.
	Addr string `json:"addr,omitempty"`
	// Status and Usage are one metered response's.
	Status int         `json:"status,omitempty"`
	Usage  *usageField `json:"usage,omitempty"`
	// Total and Cap are the run's tokens when the cap was reached.
	Total int64 `json:"total,omitempty"`
	Cap   int64 `json:"cap,omitempty"`
}

type usageField struct {
	Input         int64 `json:"input"`
	CacheCreation int64 `json:"cache_creation"`
	CacheRead     int64 `json:"cache_read"`
	Output        int64 `json:"output"`
}

func (u usageField) tokens() domain.TokenUsage {
	return domain.TokenUsage{Input: u.Input, CacheCreation: u.CacheCreation, CacheRead: u.CacheRead, Output: u.Output}
}

func fieldOf(u domain.TokenUsage) *usageField {
	return &usageField{Input: u.Input, CacheCreation: u.CacheCreation, CacheRead: u.CacheRead, Output: u.Output}
}

// encode is the line for e, newline included.
func encode(e entry) []byte {
	// Marshal fails only on types that cannot be encoded; entry has none.
	b, _ := json.Marshal(e)
	return append(b, '\n')
}

// Report is what one run's proxy metered: every token of every response it
// forwarded, and whether it refused the run more.
type Report struct {
	Tokens domain.TokenUsage
	Capped bool
}

// ReadUsage reads a proxy's log and sums what it metered. A line that is
// not the proxy's, or belongs to another run, is an error: the report is
// evidence and is either whole or refused.
func ReadUsage(r io.Reader, runID string) (Report, error) {
	var report Report
	err := scan(r, runID, func(e entry) bool {
		switch e.Event {
		case eventUsage:
			report.Tokens = report.Tokens.Add(e.Usage.tokens())
		case eventCap:
			report.Capped = true
		}
		return false
	})
	if err != nil {
		return Report{}, err
	}
	return report, nil
}

// AwaitReady reads the proxy's log until its readiness line; the stream
// ending first means the proxy exited before serving.
func AwaitReady(r io.Reader, runID string) error {
	return awaitEvent(r, runID, eventReady)
}

// AwaitCap follows the proxy's log until it says the run reached its cap.
// The stream ending first, or a line that is not this run's, is an error.
func AwaitCap(r io.Reader, runID string) error {
	return awaitEvent(r, runID, eventCap)
}

func awaitEvent(r io.Reader, runID, event string) error {
	seen := false
	err := scan(r, runID, func(e entry) bool {
		seen = e.Event == event
		return seen
	})
	if err != nil || seen {
		return err
	}
	return fmt.Errorf("model: proxy output ended before its %s line", event)
}

// scan decodes each line in order and hands it to visit until visit says
// stop or the stream ends.
func scan(r io.Reader, runID string, visit func(entry) (stop bool)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 512), maxLineBytes)
	for n := 1; sc.Scan(); n++ {
		e, err := decode(sc.Bytes(), runID)
		if err != nil {
			return fmt.Errorf("model: log line %d: %w", n, err)
		}
		if visit(e) {
			return nil
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("model: read log: %w", err)
	}
	return nil
}

func decode(line []byte, runID string) (entry, error) {
	var e entry
	if err := json.Unmarshal(line, &e); err != nil {
		return entry{}, err
	}
	if e.Run != runID {
		return entry{}, fmt.Errorf("line of run %q in the log of run %q", e.Run, runID)
	}
	switch e.Event {
	case eventReady, eventCap:
	case eventUsage:
		if e.Usage == nil || !e.Usage.valid() {
			return entry{}, errors.New("usage line without a valid usage")
		}
	default:
		return entry{}, fmt.Errorf("unknown event %q", e.Event)
	}
	return e, nil
}

func (u usageField) valid() bool {
	return u.Input >= 0 && u.CacheCreation >= 0 && u.CacheRead >= 0 && u.Output >= 0
}
