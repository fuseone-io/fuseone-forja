package egress

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// The events of the gateway's log, one JSON object per line on stdout.
const (
	eventReady  = "ready"
	eventDenied = "denied"
)

// maxLineBytes bounds one log line on read. A line the gateway writes is
// far shorter: the destination is cut to MaxDestinationBytes.
const maxLineBytes = 4 << 10

// entry is one line of the log. The writer and the reader of the format
// are this package, so the two cannot drift apart.
type entry struct {
	Event       string            `json:"event"`
	Run         string            `json:"run"`
	At          time.Time         `json:"at"`
	Kind        domain.EgressKind `json:"kind,omitempty"`
	Destination string            `json:"destination,omitempty"`
}

// encode is the line for e, newline included: the JSON encoder escapes
// every control character, so a destination cannot start a line of its
// own.
func encode(e entry) []byte {
	// Marshal fails only on types that cannot be encoded; entry has none.
	b, _ := json.Marshal(e)
	return append(b, '\n')
}

// ReadDenials reads a gateway's log and returns what it refused. A line
// that is not the gateway's, or belongs to another run, is an error: the
// report is evidence and is either whole or refused.
func ReadDenials(r io.Reader, runID string) (domain.EgressReport, error) {
	var report domain.EgressReport
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 512), maxLineBytes)
	for n := 1; sc.Scan(); n++ {
		e, err := decode(sc.Bytes(), runID)
		if err != nil {
			return domain.EgressReport{}, fmt.Errorf("egress: log line %d: %w", n, err)
		}
		if e.Event == eventDenied {
			report.Record(domain.EgressDenial{At: e.At, Kind: e.Kind, Destination: e.Destination})
		}
	}
	if err := sc.Err(); err != nil {
		return domain.EgressReport{}, fmt.Errorf("egress: read log: %w", err)
	}
	return report, nil
}

// AwaitReady reads the gateway's log until its readiness line and returns
// nil; the stream ending first means the gateway exited before serving.
func AwaitReady(r io.Reader, runID string) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 512), maxLineBytes)
	for sc.Scan() {
		if e, err := decode(sc.Bytes(), runID); err == nil && e.Event == eventReady {
			return nil
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("egress: read log: %w", err)
	}
	return errors.New("egress: gateway output ended before it was ready")
}

func decode(line []byte, runID string) (entry, error) {
	var e entry
	if err := json.Unmarshal(line, &e); err != nil {
		return entry{}, err
	}
	if e.Run != runID {
		return entry{}, fmt.Errorf("line of run %q in the log of run %q", e.Run, runID)
	}
	switch {
	case e.Event == eventReady:
	case e.Event == eventDenied && (e.Kind == domain.EgressConnect || e.Kind == domain.EgressHTTP):
	default:
		return entry{}, fmt.Errorf("unknown event %q of kind %q", e.Event, e.Kind)
	}
	return e, nil
}
