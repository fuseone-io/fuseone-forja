package registry

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
	eventReady  = "ready"
	eventAccess = "access"
)

// maxLineBytes bounds one log line on read. A line the proxy writes is
// far shorter: the registry and the path are each cut to MaxRegistryBytes.
const maxLineBytes = 4 << 10

// entry is one line of the log. The writer and the reader of the format
// are this package, so the two cannot drift apart.
type entry struct {
	Event    string                 `json:"event"`
	Run      string                 `json:"run"`
	At       time.Time              `json:"at"`
	Registry string                 `json:"registry,omitempty"`
	Path     string                 `json:"path,omitempty"`
	Outcome  domain.RegistryOutcome `json:"outcome,omitempty"`
	Status   int                    `json:"status,omitempty"`
	// Addr is where a ready proxy listens.
	Addr string `json:"addr,omitempty"`
}

// encode is the line for e, newline included: the JSON encoder escapes
// every control character, so a path cannot start a line of its own.
func encode(e entry) []byte {
	// Marshal fails only on types that cannot be encoded; entry has none.
	b, _ := json.Marshal(e)
	return append(b, '\n')
}

// ReadAccesses reads a proxy's log and returns every request it served or
// refused. A line that is not the proxy's, or belongs to another run, is
// an error: the report is evidence and is either whole or refused.
func ReadAccesses(r io.Reader, runID string) (domain.RegistryReport, error) {
	var report domain.RegistryReport
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 512), maxLineBytes)
	for n := 1; sc.Scan(); n++ {
		e, err := decode(sc.Bytes(), runID)
		if err != nil {
			return domain.RegistryReport{}, fmt.Errorf("registry: log line %d: %w", n, err)
		}
		if e.Event == eventAccess {
			report.Record(domain.RegistryAccess{At: e.At, Registry: e.Registry, Path: e.Path, Outcome: e.Outcome, Status: e.Status})
		}
	}
	if err := sc.Err(); err != nil {
		return domain.RegistryReport{}, fmt.Errorf("registry: read log: %w", err)
	}
	return report, nil
}

// AwaitReady reads the proxy's log until its readiness line and returns
// nil; the stream ending first means the proxy exited before serving.
func AwaitReady(r io.Reader, runID string) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 512), maxLineBytes)
	for sc.Scan() {
		if e, err := decode(sc.Bytes(), runID); err == nil && e.Event == eventReady {
			return nil
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("registry: read log: %w", err)
	}
	return errors.New("registry: proxy output ended before it was ready")
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
	case e.Event == eventAccess && knownOutcome(e.Outcome) && e.Status >= 100 && e.Status <= 599:
	default:
		return entry{}, fmt.Errorf("unknown event %q with outcome %q and status %d", e.Event, e.Outcome, e.Status)
	}
	return e, nil
}

func knownOutcome(o domain.RegistryOutcome) bool {
	return o == domain.RegistryAllowed || o == domain.RegistryDenied
}
