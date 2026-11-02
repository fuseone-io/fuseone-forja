package workspace

import (
	"reflect"
	"testing"

	"github.com/moby/moby/api/types/container"
)

func TestVerdict_stateOfTheSidecar_decidesReadiness(t *testing.T) {
	t.Parallel()
	health := func(s container.HealthStatus) *container.Health { return &container.Health{Status: s} }
	tests := []struct {
		name   string
		state  *container.State
		probed bool
		want   ServiceResult
		done   bool
	}{
		{name: "no state yet", state: nil, probed: true},
		{name: "probe still starting", state: &container.State{Running: true, Health: health(container.Starting)}, probed: true},
		{name: "probe failing, still running", state: &container.State{Running: true, Health: health(container.Unhealthy)}, probed: true},
		{name: "healthy", state: &container.State{Running: true, Health: health(container.Healthy)}, probed: true, want: ServiceResult{Ready: true}, done: true},
		{name: "exited while probed", state: &container.State{ExitCode: 1, Health: health(container.Starting)}, probed: true,
			want: ServiceResult{Reason: "service postgres exited with code 1"}, done: true},
		{name: "exited with a stale healthy", state: &container.State{ExitCode: 3, Health: health(container.Healthy)}, probed: true,
			want: ServiceResult{Reason: "service postgres exited with code 3"}, done: true},
		{name: "unprobed and running", state: &container.State{Running: true}, probed: false, want: ServiceResult{Ready: true}, done: true},
		{name: "unprobed and exited", state: &container.State{ExitCode: 0}, probed: false,
			want: ServiceResult{Reason: "service postgres exited with code 0"}, done: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, done := verdict("postgres", tt.state, tt.probed)
			if done != tt.done || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("verdict = (%+v, %v), want (%+v, %v)", got, done, tt.want, tt.done)
			}
		})
	}
}

func ports(ps ...string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, p := range ps {
		set[p] = struct{}{}
	}
	return set
}

func TestReadinessOf_image_probeOnlyWhereTheImageHasNone(t *testing.T) {
	t.Parallel()
	const pg = "grep -qsiE ':1538 [0-9A-F]+:[0-9A-F]+ 0A' /proc/net/tcp /proc/net/tcp6"
	const redis = "grep -qsiE ':18EB [0-9A-F]+:[0-9A-F]+ 0A' /proc/net/tcp /proc/net/tcp6"
	tests := []struct {
		name   string
		ports  map[string]struct{}
		own    []string
		probe  []string
		probed bool
	}{
		{name: "one tcp port", ports: ports("5432/tcp"), probe: []string{"CMD-SHELL", pg}, probed: true},
		{name: "every tcp port, in order, udp left out", ports: ports("6379/tcp", "53/udp", "5432/tcp"),
			probe: []string{"CMD-SHELL", pg + " && " + redis}, probed: true},
		{name: "own healthcheck kept", ports: ports("5432/tcp"), own: []string{"CMD", "pg_isready"}, probe: nil, probed: true},
		{name: "own healthcheck disabled", ports: ports("5432/tcp"), own: []string{"NONE"}, probe: []string{"CMD-SHELL", pg}, probed: true},
		{name: "no port, no healthcheck", ports: ports(), probe: nil, probed: false},
		{name: "udp only", ports: ports("53/udp"), probe: nil, probed: false},
		{name: "no config", ports: nil, probe: nil, probed: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := readinessOf(tt.ports, tt.own)
			if err != nil {
				t.Fatalf("readinessOf: %v", err)
			}
			var test []string
			if got.probe != nil {
				test = got.probe.Test
			}
			if !reflect.DeepEqual(test, tt.probe) || got.probed != tt.probed {
				t.Fatalf("readinessOf = {probe %q, probed %v}, want {%q, %v}", test, got.probed, tt.probe, tt.probed)
			}
			if got.probe != nil && got.probe.Interval < 1e6 {
				t.Errorf("probe interval %v, the daemon refuses less than 1ms", got.probe.Interval)
			}
		})
	}
}

func TestReadinessOf_portNotUnderstood_isAnError(t *testing.T) {
	t.Parallel()
	if _, err := readinessOf(ports("http/tcp"), nil); err == nil {
		t.Fatal("readinessOf(http/tcp) = nil error, want the port named: a port skipped is a probe that passes too early")
	}
}

func TestEnvList_contractEnv_sortedKeyValue(t *testing.T) {
	t.Parallel()
	got := envList(map[string]string{"POSTGRES_HOST_AUTH_METHOD": "trust", "A": "1=2"})
	if want := []string{"A=1=2", "POSTGRES_HOST_AUTH_METHOD=trust"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("envList = %q, want %q", got, want)
	}
}
