package workspace

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
)

// The control plane's own probe runs often, so a sidecar is seen ready
// soon after it listens; the daemon refuses an interval under 1ms.
const (
	probeInterval = 200 * time.Millisecond
	probeTimeout  = 5 * time.Second
)

// readiness is how one sidecar's readiness is decided: probe is the
// healthcheck to set on it, nil to keep the image's own or to have none,
// and probed says whether the daemon will report a health state at all.
type readiness struct {
	probe  *container.HealthConfig
	probed bool
}

// readinessOf keeps an image's own healthcheck. Without one, an image
// that exposes TCP ports gets a probe that passes only when every one of
// them is in LISTEN in the sidecar's own network namespace, and an image
// that exposes none is ready once running.
func readinessOf(exposed map[string]struct{}, own []string) (readiness, error) {
	if len(own) > 0 && own[0] != "NONE" {
		return readiness{probed: true}, nil
	}
	ports, err := tcpPorts(exposed)
	if err != nil || len(ports) == 0 {
		return readiness{}, err
	}
	return readiness{probe: &container.HealthConfig{
		Test:     []string{"CMD-SHELL", listenProbe(ports)},
		Interval: probeInterval,
		Timeout:  probeTimeout,
	}, probed: true}, nil
}

// tcpPorts reads the TCP ports out of an image's ExposedPorts, "5432/tcp"
// or a bare "5432", in ascending order.
func tcpPorts(exposed map[string]struct{}) ([]uint16, error) {
	ports := make([]uint16, 0, len(exposed))
	for spec := range exposed {
		port, proto, _ := strings.Cut(spec, "/")
		if proto != "" && proto != "tcp" {
			continue
		}
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil {
			return nil, fmt.Errorf("exposed port %q not understood: %w", spec, err)
		}
		ports = append(ports, uint16(n))
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	return ports, nil
}

// listenProbe exits 0 only when every port has a socket in state 0A
// (LISTEN) in /proc/net/tcp or tcp6, where the local port is in hex. The
// decision is grep's exit code; nothing is dialled.
func listenProbe(ports []uint16) string {
	checks := make([]string, 0, len(ports))
	for _, p := range ports {
		checks = append(checks, fmt.Sprintf("grep -qsiE ':%04X [0-9A-F]+:[0-9A-F]+ 0A' /proc/net/tcp /proc/net/tcp6", p))
	}
	return strings.Join(checks, " && ")
}

// envList is a service's env as the daemon takes it, in key order.
func envList(env map[string]string) []string {
	list := make([]string, 0, len(env))
	for k, v := range env {
		list = append(list, k+"="+v)
	}
	sort.Strings(list)
	return list
}
