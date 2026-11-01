package workspace

// MaxStreamBytes is how much of each stream Exec keeps: the last bytes,
// which is what a maintainer reads first on a red gate. The repository
// controls what a gate prints, so the bound is applied as the bytes
// arrive, never after the whole stream sits in memory.
const MaxStreamBytes = 64 << 10

// tail is a writer that keeps the last max bytes written to it and counts
// the rest.
type tail struct {
	max   int
	buf   []byte
	total int64
}

func newTail(max int) *tail {
	return &tail{max: max, buf: make([]byte, 0, max)}
}

// Write accepts every byte and keeps only the last max of them.
func (t *tail) Write(p []byte) (int, error) {
	t.total += int64(len(p))
	if len(p) >= t.max {
		t.buf = append(t.buf[:0], p[len(p)-t.max:]...)
		return len(p), nil
	}
	if over := len(t.buf) + len(p) - t.max; over > 0 {
		t.buf = t.buf[:copy(t.buf, t.buf[over:])]
	}
	t.buf = append(t.buf, p...)
	return len(p), nil
}

// Bytes is the tail kept so far.
func (t *tail) Bytes() []byte { return t.buf }

// Truncated reports whether more than max bytes were written.
func (t *tail) Truncated() bool { return t.total > int64(t.max) }
