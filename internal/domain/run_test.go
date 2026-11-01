package domain_test

import (
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

func validRun() domain.Run {
	return domain.Run{
		ID:         "run-1",
		Tool:       domain.ToolImplement,
		Repository: "fuseone-io/fuseone-forja",
		Issue:      5,
		State:      domain.StateRunning,
		StartedAt:  time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
	}
}

func TestRun_validate_rejectsEmptyIDUnknownToolAndUnknownState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(r *domain.Run)
		wantErr bool
	}{
		{name: "valid run", mutate: func(*domain.Run) {}, wantErr: false},
		{name: "valid run, terminal", mutate: func(r *domain.Run) {
			finished := r.StartedAt.Add(time.Minute)
			r.State = domain.StateSucceeded
			r.FinishedAt = &finished
		}, wantErr: false},
		{name: "empty id", mutate: func(r *domain.Run) { r.ID = "" }, wantErr: true},
		{name: "unknown tool", mutate: func(r *domain.Run) { r.Tool = "forja.deploy" }, wantErr: true},
		{name: "empty tool", mutate: func(r *domain.Run) { r.Tool = "" }, wantErr: true},
		{name: "unknown state", mutate: func(r *domain.Run) { r.State = "paused" }, wantErr: true},
		{name: "empty state", mutate: func(r *domain.Run) { r.State = "" }, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := validRun()
			tt.mutate(&r)
			err := r.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRun_validate_everyDeclaredToolAndStateIsAccepted(t *testing.T) {
	t.Parallel()

	tools := []domain.Tool{
		domain.ToolDoctor, domain.ToolPlan, domain.ToolImplement, domain.ToolVerify, domain.ToolStatus,
	}
	states := []domain.RunState{
		domain.StateQueued, domain.StateRunning, domain.StateSucceeded, domain.StateFailed,
	}
	for _, tool := range tools {
		for _, state := range states {
			r := validRun()
			r.Tool = tool
			r.State = state
			if err := r.Validate(); err != nil {
				t.Errorf("Validate() tool=%q state=%q: unexpected error %v", tool, state, err)
			}
		}
	}
}
