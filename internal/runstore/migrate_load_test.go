package runstore

import (
	"strings"
	"testing"
	"testing/fstest"
)

func sqlFile(body string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(body)} }

// A migration set a developer got wrong is an error naming the culprit,
// not a panic in the middle of a serve.
func TestLoadMigrations_malformedSet_errorNamingTheFile(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		files fstest.MapFS
		want  string
	}{
		"no prefix":       {fstest.MapFS{"0001_runs.sql": sqlFile("a"), "add_index.sql": sqlFile("b")}, "add_index.sql"},
		"dash, not under": {fstest.MapFS{"0001_runs.sql": sqlFile("a"), "0002-index.sql": sqlFile("b")}, "0002-index.sql"},
		"version zero":    {fstest.MapFS{"0000_runs.sql": sqlFile("a")}, "0000_runs.sql"},
		"version twice":   {fstest.MapFS{"0001_runs.sql": sqlFile("a"), "001_again.sql": sqlFile("b")}, "version 1"},
		"no migration":    {fstest.MapFS{}, "no migration"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := loadMigrations(tt.files)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("loadMigrations = %v, %v; want an error naming %q", got, err, tt.want)
			}
		})
	}
}

func TestLoadMigrations_wellFormedSet_oldestFirst(t *testing.T) {
	t.Parallel()
	got, err := loadMigrations(fstest.MapFS{"0010_later.sql": sqlFile("later"), "0002_first.sql": sqlFile("first")})
	if err != nil || len(got) != 2 || got[0].Version != 2 || got[0].SQL != "first" || got[1].Version != 10 {
		t.Fatalf("loadMigrations = %+v, %v; want 2 then 10 with their bodies", got, err)
	}
}

// What Migrations hands out is the caller's: changing it changes nothing
// the next migrate applies.
func TestMigrations_callerMutation_notSeenByTheNext(t *testing.T) {
	t.Parallel()
	first := Migrations()
	first[0].SQL = "DROP TABLE forja_runs"
	if Migrations()[0].SQL == "DROP TABLE forja_runs" {
		t.Fatal("Migrations hands out the embedded set itself")
	}
}
