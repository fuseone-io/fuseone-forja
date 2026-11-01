package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

func TestClaimRef_namesTheIssueUnderForjaClaims(t *testing.T) {
	t.Parallel()
	if got, want := domain.ClaimRef(12), "refs/forja/claims/12"; got != want {
		t.Fatalf("ClaimRef(12) = %q, want %q", got, want)
	}
}

func TestLeaseFor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		wallClock  time.Duration
		configured time.Duration
		want       time.Duration
		wantErr    bool
	}{
		{name: "default is wall_clock plus the margin", wallClock: 30 * time.Minute, want: 45 * time.Minute},
		{name: "no limits, no configuration: one hour", want: 60 * time.Minute},
		{name: "configured longer than wall_clock wins", wallClock: 30 * time.Minute, configured: 50 * time.Minute, want: 50 * time.Minute},
		{name: "configured shorter than wall_clock is an error", wallClock: 30 * time.Minute, configured: 20 * time.Minute, wantErr: true},
		{name: "configured equal to wall_clock is an error", wallClock: 30 * time.Minute, configured: 30 * time.Minute, wantErr: true},
		{name: "configured with no limits wins", configured: 10 * time.Minute, want: 10 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := domain.LeaseFor(domain.Limits{WallClock: tt.wallClock}, tt.configured)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("LeaseFor = %v, want an error", got)
				}
				for _, want := range []string{tt.wallClock.String(), tt.configured.String()} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not name %s", err, want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("LeaseFor: %v", err)
			}
			if got != tt.want {
				t.Fatalf("LeaseFor = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClaim_expired_atAndAfterExpiresAtOnly(t *testing.T) {
	t.Parallel()
	expires := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	c := domain.Claim{ExpiresAt: expires}
	tests := []struct {
		name string
		now  time.Time
		want bool
	}{
		{name: "before", now: expires.Add(-time.Second), want: false},
		{name: "at", now: expires, want: true},
		{name: "after", now: expires.Add(time.Second), want: true},
	}
	for _, tt := range tests {
		if got := c.Expired(tt.now); got != tt.want {
			t.Errorf("%s: Expired = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestClaimHeldError_isErrClaimHeldAndNamesHolder(t *testing.T) {
	t.Parallel()
	holder := domain.Claim{
		RunID:      "run-holder",
		Repository: "fuseone-io/fuseone-forja",
		Issue:      12,
		ExpiresAt:  time.Date(2026, 11, 1, 12, 45, 0, 0, time.UTC),
	}
	var err error = &domain.ClaimHeldError{Holder: holder}
	if !errors.Is(err, domain.ErrClaimHeld) {
		t.Fatalf("errors.Is(ClaimHeldError, ErrClaimHeld) = false")
	}
	for _, want := range []string{"run-holder", "2026-11-01T12:45:00Z", "12"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not carry %q", err, want)
		}
	}
	var held *domain.ClaimHeldError
	if !errors.As(err, &held) || held.Holder.RunID != "run-holder" {
		t.Fatalf("errors.As did not recover the holder: %v", err)
	}
	if errors.Is(domain.ErrClaimLost, domain.ErrClaimHeld) || errors.Is(domain.ErrClaimUnreadable, domain.ErrClaimHeld) {
		t.Fatal("ErrClaimLost or ErrClaimUnreadable match ErrClaimHeld")
	}
}

func TestClaimRequest_validate(t *testing.T) {
	t.Parallel()
	valid := domain.ClaimRequest{Repository: "fuseone-io/fuseone-forja", Issue: 12, RunID: "run-1", Lease: time.Hour}
	tests := []struct {
		name    string
		mutate  func(r *domain.ClaimRequest)
		wantErr bool
	}{
		{name: "valid", mutate: func(*domain.ClaimRequest) {}},
		{name: "no repository", mutate: func(r *domain.ClaimRequest) { r.Repository = "" }, wantErr: true},
		{name: "issue zero", mutate: func(r *domain.ClaimRequest) { r.Issue = 0 }, wantErr: true},
		{name: "issue negative", mutate: func(r *domain.ClaimRequest) { r.Issue = -3 }, wantErr: true},
		{name: "no run id", mutate: func(r *domain.ClaimRequest) { r.RunID = "" }, wantErr: true},
		{name: "no lease", mutate: func(r *domain.ClaimRequest) { r.Lease = 0 }, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := valid
			tt.mutate(&req)
			if err := req.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
