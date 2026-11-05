package domain

import (
	"cmp"
	"fmt"
	"time"
)

// RunFilter selects runs. A zero field selects every run: the zero filter
// matches them all. Since and Until bound StartedAt, Since inclusive and
// Until exclusive.
type RunFilter struct {
	// Client keeps the runs of that client only, compared exactly; empty
	// is every client's, which only the operator may ask for.
	Client     string
	Repository string
	Tool       Tool
	State      RunState
	Since      time.Time
	Until      time.Time
}

// Validate reports whether every field the filter sets is one a run can
// have, and whether its window, when both ends are set, is not empty.
func (f RunFilter) Validate() error {
	if !IsText(f.Repository) {
		return fmt.Errorf("repository %q is not text", f.Repository)
	}
	if !IsText(f.Client) {
		return fmt.Errorf("client %q is not text", f.Client)
	}
	if f.Tool != "" && !knownTool(f.Tool) {
		return fmt.Errorf("unknown tool %q", f.Tool)
	}
	if f.State != "" && !knownState(f.State) {
		return fmt.Errorf("unknown run state %q", f.State)
	}
	if !f.Since.IsZero() && !f.Until.IsZero() && !f.Since.Before(f.Until) {
		return fmt.Errorf("since %s is not before until %s", f.Since.Format(time.RFC3339Nano), f.Until.Format(time.RFC3339Nano))
	}
	return nil
}

// Match reports whether r passes every field the filter sets.
func (f RunFilter) Match(r Run) bool {
	switch {
	case f.Client != "" && r.Client != f.Client:
		return false
	case f.Repository != "" && r.Repository != f.Repository:
		return false
	case f.Tool != "" && r.Tool != f.Tool:
		return false
	case f.State != "" && r.State != f.State:
		return false
	}
	return f.inWindow(r.StartedAt)
}

func (f RunFilter) inWindow(t time.Time) bool {
	if !f.Since.IsZero() && t.Before(f.Since) {
		return false
	}
	return f.Until.IsZero() || t.Before(f.Until)
}

// CompareNewestFirst orders runs by StartedAt, latest first, and runs that
// started together by ID, highest first, so that no two runs tie.
func CompareNewestFirst(a, b Run) int {
	if c := b.StartedAt.Compare(a.StartedAt); c != 0 {
		return c
	}
	return cmp.Compare(b.ID, a.ID)
}
