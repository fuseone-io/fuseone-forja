package claim

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fuseone-io/fuseone-forja/internal/domain"
)

// claimPath is the one file of a claim commit.
const claimPath = "claim.json"

// claimVersion is the format of claim.json this package reads and writes.
// Another version is not understood and so never taken over.
const claimVersion = 1

// claimFile is the wire form of claim.json. Times are RFC3339 in UTC.
type claimFile struct {
	Version    int             `json:"version"`
	RunID      string          `json:"run_id"`
	Repository string          `json:"repository"`
	Issue      int             `json:"issue"`
	CreatedAt  time.Time       `json:"created_at"`
	ExpiresAt  time.Time       `json:"expires_at"`
	Supersedes *supersedesFile `json:"supersedes,omitempty"`
}

type supersedesFile struct {
	RunID     string    `json:"run_id"`
	SHA       string    `json:"sha"`
	ExpiresAt time.Time `json:"expires_at"`
}

func encodeClaim(c domain.Claim) ([]byte, error) {
	file := claimFile{
		Version: claimVersion, RunID: c.RunID, Repository: c.Repository, Issue: c.Issue,
		CreatedAt: c.CreatedAt.UTC(), ExpiresAt: c.ExpiresAt.UTC(),
	}
	if c.Supersedes != nil {
		file.Supersedes = &supersedesFile{RunID: c.Supersedes.RunID, SHA: c.Supersedes.SHA, ExpiresAt: c.Supersedes.ExpiresAt.UTC()}
	}
	body, err := json.Marshal(file)
	if err != nil {
		return nil, fmt.Errorf("encode claim: %w", err)
	}
	return body, nil
}

// decodeClaim reads claim.json found under the ref of issue in repo and
// refuses anything that does not describe exactly that issue.
func decodeClaim(body []byte, repo string, issue int) (domain.Claim, error) {
	var file claimFile
	if err := json.Unmarshal(body, &file); err != nil {
		return domain.Claim{}, fmt.Errorf("decode claim: %w", err)
	}
	switch {
	case file.Version != claimVersion:
		return domain.Claim{}, fmt.Errorf("claim version %d is not %d", file.Version, claimVersion)
	case file.RunID == "":
		return domain.Claim{}, errors.New("claim has no run id")
	case file.ExpiresAt.IsZero():
		return domain.Claim{}, errors.New("claim has no expiry")
	case file.Repository != repo || file.Issue != issue:
		return domain.Claim{}, fmt.Errorf("claim is for issue %d of %s", file.Issue, file.Repository)
	}
	c := domain.Claim{
		RunID: file.RunID, Repository: file.Repository, Issue: file.Issue,
		CreatedAt: file.CreatedAt, ExpiresAt: file.ExpiresAt,
	}
	if file.Supersedes != nil {
		c.Supersedes = &domain.ClaimSummary{RunID: file.Supersedes.RunID, SHA: file.Supersedes.SHA, ExpiresAt: file.Supersedes.ExpiresAt}
	}
	return c, nil
}
