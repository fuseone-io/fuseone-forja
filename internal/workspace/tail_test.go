package workspace

import (
	"strings"
	"testing"
)

func TestTail_write_keepsOnlyTheLastMaxBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		writes        []string
		want          string
		wantTruncated bool
	}{
		{name: "under the cap", writes: []string{"abc"}, want: "abc"},
		{name: "exactly the cap", writes: []string{"abcdefgh"}, want: "abcdefgh"},
		{name: "one write over the cap", writes: []string{"0123456789"}, want: "23456789", wantTruncated: true},
		{name: "one write far over the cap", writes: []string{strings.Repeat("x", 100) + "TAIL"}, want: "xxxxTAIL", wantTruncated: true},
		{name: "many small writes over the cap", writes: []string{"ab", "cd", "ef", "gh", "ij", "kl"}, want: "efghijkl", wantTruncated: true},
		{name: "small writes then a large one", writes: []string{"ab", "0123456789"}, want: "23456789", wantTruncated: true},
		{name: "empty write", writes: []string{""}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := newTail(8)
			for _, s := range tt.writes {
				n, err := w.Write([]byte(s))
				if err != nil || n != len(s) {
					t.Fatalf("Write(%q) = (%d, %v), want (%d, nil): a tail writer accepts every byte", s, n, err, len(s))
				}
			}
			if got := string(w.Bytes()); got != tt.want {
				t.Errorf("Bytes() = %q, want %q", got, tt.want)
			}
			if got := w.Truncated(); got != tt.wantTruncated {
				t.Errorf("Truncated() = %v, want %v", got, tt.wantTruncated)
			}
		})
	}
}

// A writer that keeps the tail must not keep growing with the input: that
// is the whole point of it.
func TestTail_hugeInput_memoryStaysBounded(t *testing.T) {
	t.Parallel()
	w := newTail(1024)
	chunk := []byte(strings.Repeat("y", 4096))
	for range 1000 {
		if _, err := w.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if got := cap(w.buf); got > 2*1024+len(chunk) {
		t.Fatalf("buffer capacity = %d after 4 MiB written, want it bounded by the cap and one chunk", got)
	}
	if len(w.Bytes()) != 1024 || !w.Truncated() {
		t.Fatalf("Bytes() len %d, Truncated %v; want 1024 and true", len(w.Bytes()), w.Truncated())
	}
}
