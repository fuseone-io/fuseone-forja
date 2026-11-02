package workspace

import (
	"errors"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/system"
)

func TestRuntimeAvailable_configuredAgainstRegistered(t *testing.T) {
	t.Parallel()
	registered := map[string]system.RuntimeWithStatus{"runc": {}, "io.containerd.runc.v2": {}, "runsc": {}}
	tests := []struct {
		name       string
		registered map[string]system.RuntimeWithStatus
		want       string
		refused    bool
	}{
		{name: "empty is the daemon's default", registered: registered, want: ""},
		{name: "empty on a daemon listing none", registered: nil, want: ""},
		{name: "registered", registered: registered, want: "runsc"},
		{name: "registered non-default runc", registered: registered, want: "io.containerd.runc.v2"},
		{name: "not registered", registered: map[string]system.RuntimeWithStatus{"runc": {}}, want: "runsc", refused: true},
		{name: "daemon listing none", registered: nil, want: "runsc", refused: true},
		{name: "case differs", registered: registered, want: "RUNSC", refused: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := runtimeAvailable(tt.registered, tt.want)
			if !tt.refused {
				if err != nil {
					t.Fatalf("runtimeAvailable(%q) = %v, want nil", tt.want, err)
				}
				return
			}
			if !errors.Is(err, ErrRuntimeUnavailable) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("runtimeAvailable(%q) = %v, want ErrRuntimeUnavailable naming it", tt.want, err)
			}
		})
	}
}
