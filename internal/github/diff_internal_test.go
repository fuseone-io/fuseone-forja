package github

import (
	"reflect"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// A status letter the diff should never print without rename detection,
// or a record cut short, is an error: no change is ever dropped.
func TestJoinDiff_unknownStatusOrBrokenRecord_error(t *testing.T) {
	t.Parallel()
	for name, tt := range map[string]struct{ status, numstat string }{
		"rename":              {"R100\x00a\x00b\x00", "1\t1\x00a\x00b\x00"},
		"copy":                {"C\x00a\x00", "1\t1\ta\x00"},
		"unmerged":            {"U\x00a\x00", "1\t1\ta\x00"},
		"unknown":             {"X\x00a\x00", "1\t1\ta\x00"},
		"status without path": {"M\x00", ""},
		"no line counts":      {"M\x00a\x00", ""},
		"counts of another":   {"M\x00a\x00", "1\t1\tb\x00"},
		"counts not numbers":  {"M\x00a\x00", "x\t1\ta\x00"},
		"counts cut short":    {"M\x00a\x00", "1\x00"},
	} {
		if got, err := joinDiff([]byte(tt.status), []byte(tt.numstat)); err == nil {
			t.Errorf("%s: joinDiff = %+v, nil; want an error", name, got)
		}
	}
}

func TestJoinDiff_records_inNameStatusOrderWithCounts(t *testing.T) {
	t.Parallel()
	got, err := joinDiff(
		[]byte("A\x00b\x00D\x00a\x00M\x00c\x00T\x00d\x00"),
		[]byte("0\t4\ta\x00-\t-\tb\x002\t1\tc\x001\t1\td\x00"),
	)
	if err != nil {
		t.Fatalf("joinDiff: %v", err)
	}
	want := []domain.FileChange{
		{Path: "b", Kind: domain.ChangeAdded, Binary: true},
		{Path: "a", Kind: domain.ChangeDeleted, Deleted: 4},
		{Path: "c", Kind: domain.ChangeModified, Added: 2, Deleted: 1},
		{Path: "d", Kind: domain.ChangeTypeChanged, Added: 1, Deleted: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("joinDiff = %+v, want %+v", got, want)
	}
}
