package gates

import (
	"context"
	"fmt"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// ServiceResult is what starting one service left behind. Ready is the
// control plane's own reading of the sidecar's state, never the
// workspace's word; Reason is its one-line account when not ready, and Log
// the tail of the sidecar's own output, bounded by the workspace, which
// says so in Truncated.
type ServiceResult struct {
	Ready     bool
	Reason    string
	Log       []byte
	Truncated bool
}

// services starts each service of the contract the first time a gate
// that is about to run needs it, and never again in the same run.
type services struct {
	ws        Workspace
	declared  map[string]domain.Service
	readiness time.Duration
	started   map[string]bool
}

func newServices(ws Workspace, declared map[string]domain.Service, readiness time.Duration) *services {
	if readiness == 0 {
		readiness = DefaultReadiness
	}
	return &services{ws: ws, declared: declared, readiness: readiness, started: map[string]bool{}}
}

// start starts, in order, each of needs not started yet, and stops at the
// first that is not ready, whose result it returns.
func (s *services) start(ctx context.Context, needs []string) (ServiceResult, error) {
	for _, name := range needs {
		if s.started[name] {
			continue
		}
		svc, ok := s.declared[name]
		if !ok {
			return ServiceResult{}, fmt.Errorf("service %s is not declared under services", name)
		}
		res, err := s.ws.StartService(ctx, name, svc, s.readiness)
		if err != nil {
			return res, fmt.Errorf("start service %s: %w", name, err)
		}
		s.started[name] = true
		if !res.Ready {
			return res, nil
		}
	}
	return ServiceResult{Ready: true}, nil
}
