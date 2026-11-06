package claim_test

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/fuseone-io/fuseone-forja/internal/claim"
)

func TestJournal_putRemoveList_roundTrip(t *testing.T) {
	t.Parallel()
	journal := &claim.Journal{Dir: filepath.Join(t.TempDir(), "claims")}
	entries := []claim.Entry{
		{Repository: "fuseone-io/fuseone-forja", Issue: 12, RunID: "run-a", SHA: "aaa"},
		{Repository: "fuseone-io/fuseone-forja", Issue: 13, RunID: "run-b", SHA: "bbb"},
		{Repository: "other-org/other-repo", Issue: 12, RunID: "run-c", SHA: "ccc"},
	}
	for _, e := range entries {
		if err := journal.Put(e); err != nil {
			t.Fatalf("Put(%+v): %v", e, err)
		}
	}
	got, err := journal.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	sortEntries(got)
	sortEntries(entries)
	if !reflect.DeepEqual(got, entries) {
		t.Fatalf("List = %+v, want %+v", got, entries)
	}

	if err := journal.Remove("fuseone-io/fuseone-forja", 12); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := journal.Remove("fuseone-io/fuseone-forja", 12); err != nil {
		t.Fatalf("Remove of an absent entry: %v, want nil", err)
	}
	got, err = journal.List()
	if err != nil {
		t.Fatalf("List after Remove: %v", err)
	}
	sortEntries(got)
	if want := entries[1:]; !reflect.DeepEqual(got, want) {
		t.Fatalf("List after Remove = %+v, want %+v", got, want)
	}
}

func sortEntries(es []claim.Entry) {
	sort.Slice(es, func(i, j int) bool {
		if es[i].Repository != es[j].Repository {
			return es[i].Repository < es[j].Repository
		}
		return es[i].Issue < es[j].Issue
	})
}

// The same repository and issue is one claim: a second Put replaces the
// entry instead of leaving two files that disagree.
func TestJournal_putSameIssueTwice_lastEntryWins(t *testing.T) {
	t.Parallel()
	journal := &claim.Journal{Dir: t.TempDir()}
	first := claim.Entry{Repository: "fuseone-io/fuseone-forja", Issue: 12, RunID: "run-a", SHA: "aaa"}
	second := claim.Entry{Repository: "fuseone-io/fuseone-forja", Issue: 12, RunID: "run-b", SHA: "bbb"}
	for _, e := range []claim.Entry{first, second} {
		if err := journal.Put(e); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	got, err := journal.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0] != second {
		t.Fatalf("List = %+v, want only %+v", got, second)
	}
}

func TestJournal_missingDirectory_listsNothingAndPutCreatesIt(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "not", "yet")
	journal := &claim.Journal{Dir: dir}
	got, err := journal.List()
	if err != nil || len(got) != 0 {
		t.Fatalf("List on a missing directory = %+v, %v; want empty and no error", got, err)
	}
	if err := journal.Put(claim.Entry{Repository: "o/r", Issue: 1, RunID: "run", SHA: "s"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("Put did not create the directory: %v", err)
	}
}

// A file in the directory that is not a complete journal entry is a
// failure to report, not a claim to forget: without every field the ref it
// names cannot be verified, and a silent skip would leave it behind.
func TestJournal_undecodableEntry_listFailsNamingTheFile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
	}{
		{name: "not json", body: "not json"},
		{name: "no run id and no sha", body: `{"repository":"o/r","issue":7}`},
		{name: "issue zero", body: `{"repository":"o/r","issue":0,"run_id":"run","sha":"s"}`},
		{name: "no repository", body: `{"issue":7,"run_id":"run","sha":"s"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "o__r__7.json"), []byte(tt.body), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			_, err := (&claim.Journal{Dir: dir}).List()
			if err == nil {
				t.Fatal("List succeeded over an entry that is not a claim")
			}
			if !strings.Contains(err.Error(), "o__r__7.json") {
				t.Fatalf("error %q does not name the file", err)
			}
		})
	}
}

// PutNew records an entry only for an issue the journal has none for: an
// entry already there, a claim this process holds, is kept as it was.
func TestJournal_putNew_issueAlreadyJournaled_entryKeptNotAdded(t *testing.T) {
	t.Parallel()
	journal := &claim.Journal{Dir: filepath.Join(t.TempDir(), "claims")}
	held := claim.Entry{Repository: "o/r", Issue: 7, RunID: "run-held", SHA: "aaa"}
	added, err := journal.PutNew(held)
	if err != nil || !added {
		t.Fatalf("PutNew on an empty journal = (%v, %v), want added", added, err)
	}
	added, err = journal.PutNew(claim.Entry{Repository: "o/r", Issue: 7, RunID: "run-other", SHA: "bbb"})
	if err != nil || added {
		t.Fatalf("PutNew over a journaled issue = (%v, %v), want not added", added, err)
	}
	got, err := journal.List()
	if err != nil || !reflect.DeepEqual(got, []claim.Entry{held}) {
		t.Fatalf("List = %+v, %v; want only %+v", got, err, held)
	}
	if files, _ := os.ReadDir(journal.Dir); len(files) != 1 {
		t.Fatalf("journal directory holds %d files, want the one entry", len(files))
	}
}

func TestJournal_noDirectoryConfigured_failsLoudly(t *testing.T) {
	t.Parallel()
	var journal claim.Journal
	if err := journal.Put(claim.Entry{Repository: "o/r", Issue: 1, RunID: "run", SHA: "s"}); err == nil {
		t.Fatal("Put with no directory succeeded")
	}
	if _, err := journal.List(); err == nil {
		t.Fatal("List with no directory succeeded")
	}
}
