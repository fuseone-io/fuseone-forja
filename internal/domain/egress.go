package domain

import "time"

// EgressKind is how a denied attempt reached the gateway.
type EgressKind string

// The two kinds of request a forward proxy receives: a tunnel, or a plain
// HTTP request in absolute form.
const (
	EgressConnect EgressKind = "connect"
	EgressHTTP    EgressKind = "http"
)

// MaxEgressDenials is how many denials a report lists. The count goes on
// past it: a workspace that retries in a loop decides how many attempts
// there are, not how much the control plane keeps.
const MaxEgressDenials = 100

// EgressDenial is one attempt to leave the workspace that the gateway
// refused. Destination is host:port only, never a path, header or body.
type EgressDenial struct {
	At          time.Time
	Kind        EgressKind
	Destination string
}

// EgressReport is what a run's gateway refused: every attempt counted, the
// first MaxEgressDenials listed in order. The zero value is an empty report.
type EgressReport struct {
	Denied []EgressDenial
	Count  int
}

// Record counts d, and lists it while the list has room.
func (r *EgressReport) Record(d EgressDenial) {
	r.Count++
	if len(r.Denied) < MaxEgressDenials {
		r.Denied = append(r.Denied, d)
	}
}

// Merge is r followed by o: both counts, and o's listed denials after r's
// while the list has room.
func (r EgressReport) Merge(o EgressReport) EgressReport {
	room := max(0, MaxEgressDenials-len(r.Denied))
	denied := append(append([]EgressDenial(nil), r.Denied...), o.Denied[:min(room, len(o.Denied))]...)
	return EgressReport{Denied: denied, Count: r.Count + o.Count}
}

// Truncated reports whether denials were counted that the list leaves out.
func (r EgressReport) Truncated() bool {
	return r.Count > len(r.Denied)
}
