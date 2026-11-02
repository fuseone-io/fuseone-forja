package gates

import "time"

// meter is the compute of one gates run on the runner's clock: the
// workspace from just before its launch and each service sidecar from just
// before its start, all until the workspace's Close returned, which takes
// the sidecars down with it.
type meter struct {
	now      func() time.Time
	launched time.Time
	sidecars []time.Time
}

func newMeter(now func() time.Time) *meter {
	return &meter{now: now, launched: now()}
}

// sidecar records that a service sidecar is about to start.
func (m *meter) sidecar() {
	m.sidecars = append(m.sidecars, m.now())
}

// stop is the compute up to now, once the workspace is closed or its
// launch returned an error.
func (m *meter) stop() time.Duration {
	end := m.now()
	total := end.Sub(m.launched)
	for _, at := range m.sidecars {
		total += end.Sub(at)
	}
	return total
}
