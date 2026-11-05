package runstore_test

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

var timeType = reflect.TypeFor[time.Time]()

// filler sets every exported field it reaches to a value of its own, so
// that a field a store drops, merges or rounds shows up as a difference.
type filler struct {
	t    *testing.T
	next int
}

func (f *filler) seed() int {
	f.next++
	return f.next
}

func (f *filler) fill(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		v.SetString(fmt.Sprintf("s%d\x00é", f.seed()))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(int64(f.seed()) * 1_000_000_007)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(f.seed()))
	case reflect.Float32, reflect.Float64:
		v.SetFloat(float64(f.seed()) + 0.5)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		f.fill(p.Elem())
		v.Set(p)
	case reflect.Slice:
		f.fillSlice(v)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		key, val := reflect.New(v.Type().Key()).Elem(), reflect.New(v.Type().Elem()).Elem()
		f.fill(key)
		f.fill(val)
		m.SetMapIndex(key, val)
		v.Set(m)
	case reflect.Struct:
		f.fillStruct(v)
	default:
		f.t.Fatalf("filler: no value for a field of kind %s (%s)", v.Kind(), v.Type())
	}
}

func (f *filler) fillSlice(v reflect.Value) {
	if v.Type().Elem().Kind() == reflect.Uint8 {
		v.SetBytes([]byte{0, byte(f.seed()), 0xff})
		return
	}
	s := reflect.MakeSlice(v.Type(), 2, 2)
	for i := range 2 {
		f.fill(s.Index(i))
	}
	v.Set(s)
}

func (f *filler) fillStruct(v reflect.Value) {
	if v.Type() == timeType {
		v.Set(reflect.ValueOf(time.Date(2026, 11, 1, 12, 0, 0, f.seed()*1001, time.UTC)))
		return
	}
	for i := range v.NumField() {
		if v.Type().Field(i).IsExported() {
			f.fill(v.Field(i))
		}
	}
}

// filledRun is a run with every exported field, however deep, set; the
// fields a store may index hold values a run can have.
func filledRun(t *testing.T, id string) domain.Run {
	t.Helper()
	var run domain.Run
	(&filler{t: t}).fill(reflect.ValueOf(&run).Elem())
	run.ID, run.Repository, run.Tool, run.State = id, "acme/full", domain.ToolVerify, domain.StateFailed
	return run
}

// What Get returns is what was put, every field of it: a field added to
// the run later is covered without a change here.
func test_everyField_roundTripsExactly(t *testing.T, open opener) {
	t.Parallel()
	ctx := context.Background()
	store := open(t)
	want := filledRun(t, "run-full")
	if err := store.Put(ctx, want); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := store.Get(ctx, "run-full")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Get =\n%+v\nwant\n%+v", got, want)
	}
	listed, err := store.List(ctx, domain.RunFilter{Repository: want.Repository})
	if err != nil || len(listed) != 1 || !reflect.DeepEqual(listed[0], want) {
		t.Fatalf("List = %+v (%v), want exactly the run put", listed, err)
	}
}

// A NUL in a string a run carries, such as a path the agent named, and a
// start to the nanosecond survive the store unchanged.
func test_nulAndNanoseconds_roundTripExactly(t *testing.T, open opener) {
	t.Parallel()
	ctx := context.Background()
	store := open(t)
	want := sampleRun("run-nul")
	want.StartedAt = time.Date(2026, 10, 31, 12, 0, 0, 123_456_789, time.UTC)
	want.Branch = "forja/5\x00tail"
	want.QA.Excluded = []string{"a\x00b.md"}
	if err := store.Put(ctx, want); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := store.Get(ctx, "run-nul")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Get = %+v (%v), want %+v", got, err, want)
	}
}

// An empty list and no list are different answers on the wire: a store
// keeps which one it was given.
func test_emptyAndNilLists_keptApart(t *testing.T, open opener) {
	t.Parallel()
	ctx := context.Background()
	store := open(t)
	empty := sampleRun("run-empty")
	empty.Stages, empty.QA.Excluded = []domain.Stage{}, []string{}
	empty.Gates.Gates = []domain.GateResult{{Name: "build", Stdout: []byte{}}}
	none := sampleRun("run-nil")
	for _, r := range []domain.Run{empty, none} {
		if err := store.Put(ctx, r); err != nil {
			t.Fatalf("Put %s: %v", r.ID, err)
		}
	}

	for _, want := range []domain.Run{empty, none} {
		got, err := store.Get(ctx, want.ID)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("Get %s = %#v (%v), want %#v", want.ID, got, err, want)
		}
	}
}

// Since is inclusive and Until exclusive to the nanosecond, whatever
// precision the store keeps its own timestamps in.
func test_list_windowBoundsToTheNanosecond(t *testing.T, open opener) {
	t.Parallel()
	ctx := context.Background()
	store := open(t)
	base := time.Date(2026, 10, 31, 12, 0, 0, 1_000, time.UTC)
	for i, id := range []string{"run-0", "run-1", "run-2", "run-3"} {
		run := sampleRun(id)
		run.StartedAt = base.Add(time.Duration(i) * time.Nanosecond)
		if err := store.Put(ctx, run); err != nil {
			t.Fatalf("Put %s: %v", id, err)
		}
	}

	tests := []struct {
		name   string
		filter domain.RunFilter
		want   []string
	}{
		{"since a nanosecond in", domain.RunFilter{Since: base.Add(time.Nanosecond)}, []string{"run-3", "run-2", "run-1"}},
		{"until a nanosecond in", domain.RunFilter{Until: base.Add(time.Nanosecond)}, []string{"run-0"}},
		{"one nanosecond wide", domain.RunFilter{Since: base.Add(2 * time.Nanosecond), Until: base.Add(3 * time.Nanosecond)}, []string{"run-2"}},
		{"until the microsecond before", domain.RunFilter{Until: base}, []string{}},
		{"since the nanosecond after the last", domain.RunFilter{Since: base.Add(4 * time.Nanosecond)}, []string{}},
	}
	for _, tt := range tests {
		got, err := store.List(ctx, tt.filter)
		if err != nil || !reflect.DeepEqual(listIDs(got), tt.want) {
			t.Errorf("%s: List = %v (%v), want %v", tt.name, listIDs(got), err, tt.want)
		}
	}
}

// The tool and the state filters each select only their runs, alone and
// together with the repository.
func test_list_toolAndStateFilters(t *testing.T, open opener) {
	t.Parallel()
	ctx := context.Background()
	store := open(t)
	runs := []struct {
		id, repo string
		tool     domain.Tool
		state    domain.RunState
	}{
		{"run-a", "acme/api", domain.ToolImplement, domain.StateRunning},
		{"run-b", "acme/api", domain.ToolVerify, domain.StateRunning},
		{"run-c", "acme/api", domain.ToolImplement, domain.StateSucceeded},
		{"run-d", "acme/web", domain.ToolImplement, domain.StateRunning},
	}
	for _, r := range runs {
		run := sampleRun(r.id)
		run.Repository, run.Tool, run.State = r.repo, r.tool, r.state
		if err := store.Put(ctx, run); err != nil {
			t.Fatalf("Put %s: %v", r.id, err)
		}
	}

	tests := []struct {
		name   string
		filter domain.RunFilter
		want   []string
	}{
		{"tool", domain.RunFilter{Tool: domain.ToolVerify}, []string{"run-b"}},
		{"state", domain.RunFilter{State: domain.StateSucceeded}, []string{"run-c"}},
		{"tool and state", domain.RunFilter{Tool: domain.ToolImplement, State: domain.StateRunning}, []string{"run-d", "run-a"}},
		{"all three", domain.RunFilter{Repository: "acme/api", Tool: domain.ToolImplement, State: domain.StateRunning}, []string{"run-a"}},
	}
	for _, tt := range tests {
		got, err := store.List(ctx, tt.filter)
		if err != nil || !reflect.DeepEqual(listIDs(got), tt.want) {
			t.Errorf("%s: List = %v (%v), want %v", tt.name, listIDs(got), err, tt.want)
		}
	}
}
